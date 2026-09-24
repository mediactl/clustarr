/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package worker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/status"
	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// These tests run a REAL ffmpeg encode against a real apiserver. They skip
// without KUBEBUILDER_ASSETS (run via `make test`) and without ffmpeg,
// using pkg/transcode's own skip pattern.
const (
	ffmpegBin  = "/usr/bin/ffmpeg"
	ffprobeBin = "/usr/bin/ffprobe"
)

var testClient client.Client

func TestMain(m *testing.M) {
	code := func() int {
		if os.Getenv("KUBEBUILDER_ASSETS") == "" {
			return m.Run()
		}
		env := &envtest.Environment{
			CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
			ErrorIfCRDPathMissing: true,
		}
		cfg, err := env.Start()
		if err != nil {
			fmt.Fprintln(os.Stderr, "envtest:", err)
			return 1
		}
		defer func() { _ = env.Stop() }()
		testClient, err = client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
		if err != nil {
			fmt.Fprintln(os.Stderr, "client:", err)
			return 1
		}
		return m.Run()
	}()
	os.Exit(code)
}

func requireCluster(t *testing.T) client.Client {
	t.Helper()
	if testClient == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	return testClient
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(ffmpegBin); err != nil {
		t.Skip("ffmpeg not present on this box")
	}
	if _, err := os.Stat(ffprobeBin); err != nil {
		t.Skip("ffprobe not present on this box")
	}
}

// fixture is one TranscodeJob's world: a namespace, a RootFolder, a
// MediaFile, a profile, a generated source clip, and a seeding hard link
// of it under /data/torrents.
type fixture struct {
	ns, job     string
	dataDir     string
	logical     string // /data/media/movies/...
	local       string // dataDir/media/movies/...
	seed        string // dataDir/torrents/... -- a hard link of the source
	bin         string // dataDir/.recycle
	original    []byte
	probeHash   string
	profileName string
	profileHash string
}

var fixtureSeq int

// fixtureOptions vary newFixtureWith from the default world.
type fixtureOptions struct {
	// videoArgs replace the source clip's video encoder arguments.
	videoArgs []string

	// createProfile creates the TranscodeProfile named name instead of the
	// typed create newFixture does. status.hash is set afterwards either way.
	createProfile func(t *testing.T, c client.Client, name string)

	// fileName replaces the source's file name, Film.2020.1080p.mkv; its
	// extension picks the source's container.
	fileName string

	// mutateProfile edits the default typed profile before it is created.
	mutateProfile func(*transcodev1alpha1.TranscodeProfile)
}

func newFixture(t *testing.T, c client.Client) *fixture {
	t.Helper()
	return newFixtureWith(t, c, fixtureOptions{})
}

func newFixtureWith(t *testing.T, c client.Client, fo fixtureOptions) *fixture {
	t.Helper()
	ctx := context.Background()
	fixtureSeq++
	fileName := fo.fileName
	if fileName == "" {
		fileName = "Film.2020.1080p.mkv"
	}
	f := &fixture{
		ns:          fmt.Sprintf("worker-%d", fixtureSeq),
		job:         "film-2020-abcd1234",
		dataDir:     t.TempDir(),
		logical:     "/data/media/movies/Film (2020)/" + fileName,
		profileName: fmt.Sprintf("worker-test-%d", fixtureSeq),
		profileHash: "cafe1234",
	}
	f.local = filepath.Join(f.dataDir, "media/movies/Film (2020)", fileName)
	f.seed = filepath.Join(f.dataDir, "torrents", fileName)
	f.bin = filepath.Join(f.dataDir, ".recycle")

	// A two-second H.264 + AAC clip: not compliant, so the plan encodes.
	require.NoError(t, os.MkdirAll(filepath.Dir(f.local), 0o755))
	videoArgs := fo.videoArgs
	if videoArgs == nil {
		videoArgs = []string{"-c:v", "libx264", "-pix_fmt", "yuv420p"}
	}
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=24:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=2",
	}
	args = append(args, videoArgs...)
	args = append(args, "-c:a", "aac", "-b:a", "96k", "-shortest", f.local)
	gen := exec.Command(ffmpegBin, args...)
	out, err := gen.CombinedOutput()
	require.NoError(t, err, string(out))
	f.original, err = os.ReadFile(f.local)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(f.seed), 0o755))
	require.NoError(t, os.Link(f.local, f.seed))

	st, err := os.Stat(f.local)
	require.NoError(t, err)
	f.probeHash = mediainfo.ProbeHash(f.logical, st.Size(), st.ModTime())

	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: f.ns}}))
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: f.ns},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: "/data/media/movies", Kind: catalogv1alpha1.RootFolderKindMovie,
			RecycleBin: catalogv1alpha1.RecycleBin{Path: "/data/.recycle"},
		},
	}))
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "film-2020", Namespace: f.ns},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef:  commonv1alpha1.MediaRef{Kind: commonv1alpha1.MediaKindMovie, Name: "film-2020"},
			Path:      f.logical,
			SizeBytes: st.Size(),
		},
	}))

	if fo.createProfile != nil {
		fo.createProfile(t, c, f.profileName)
	} else {
		tp := &transcodev1alpha1.TranscodeProfile{
			ObjectMeta: metav1.ObjectMeta{Name: f.profileName},
			Spec: transcodev1alpha1.TranscodeProfileSpec{
				Video: transcodev1alpha1.VideoSpec{Preset: "ultrafast"},
				Policy: transcodev1alpha1.PolicySpec{
					MinDuration: &metav1.Duration{Duration: 0},
					// The default clip is already an efficient x264 encode
					// of a synthetic source, and x265 ultrafast re-encodes
					// it LARGER (about 160%), so the limit is lifted here.
					// The CRD default (100) is exercised by
					// TestRunUnderTheCRDDefaultOutputLimitSwapsANormalTranscode,
					// against a source as bloated as a real remux.
					MaxOutputToSourcePercent: ptr.To[int32](10_000),
				},
			},
		}
		if fo.mutateProfile != nil {
			fo.mutateProfile(tp)
		}
		require.NoError(t, c.Create(ctx, tp))
	}
	tp := &transcodev1alpha1.TranscodeProfile{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: f.profileName}, tp))
	require.NoError(t, status.PatchProfile(ctx, c, k8s.ManagerSquasharr, tp,
		func(ac *transcodeac.TranscodeProfileStatusApplyConfiguration) { ac.WithHash(f.profileHash) }))

	f.createJob(t, c, f.probeHash)
	return f
}

// createJob creates the TranscodeJob and drives the controller's half of
// its status to a steady state, so the worker acts on an object that
// ALREADY has status -- the only way a release would be visible.
func (f *fixture) createJob(t *testing.T, c client.Client, probeHash string) {
	t.Helper()
	ctx := context.Background()
	tj := &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: f.job, Namespace: f.ns},
		Spec: transcodev1alpha1.TranscodeJobSpec{
			MediaFileRef: "film-2020", ProfileRef: f.profileName,
			SourcePath: f.logical, SourceProbeHash: probeHash,
		},
	}
	require.NoError(t, c.Create(ctx, tj))
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	require.NoError(t, status.Patch(ctx, c, k8s.ManagerSquasharr, tj,
		func(ac *transcodeac.TranscodeJobStatusApplyConfiguration) {
			ac.WithObservedGeneration(1).
				WithPhase(transcodev1alpha1.TranscodeJobPhaseRunning).
				WithPlan(transcodeac.Plan().WithMode(transcodev1alpha1.PlanModeTranscode).WithEncoder("libx265")).
				WithJobRef(f.job).
				WithAttempts(1).
				WithStartedAt(now).
				WithMessage("running")
		}))
}

func (f *fixture) options() Options {
	return Options{
		DataDir:    f.dataDir,
		FFmpegPath: ffmpegBin, FFprobePath: ffprobeBin,
		Threads: 2, ProgressInterval: 50 * time.Millisecond,
	}
}

func (f *fixture) get(t *testing.T, c client.Client) *transcodev1alpha1.TranscodeJob {
	t.Helper()
	var tj transcodev1alpha1.TranscodeJob
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: f.ns, Name: f.job}, &tj))
	return &tj
}

// processWith runs Process on the task BuildTask renders from the fixture's
// real, apiserver-defaulted objects: the same producer squasharr dispatches
// with (transcodejob's dispatch), under the caller-supplied options -- for a
// test that needs to override one (a wrapped ffmpeg, a failing verifier). A
// BuildTask error is reported as ExitInvalidSource, before Process is ever
// called, as dispatch blocks such a job InvalidSource without a task.
func (f *fixture) processWith(t *testing.T, c client.Client, o Options) Outcome {
	t.Helper()
	ctx := context.Background()
	tj := f.get(t, c)
	var tp transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: tj.Spec.ProfileRef}, &tp))
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: tj.Namespace, Name: tj.Spec.MediaFileRef}, &mf))
	var folders catalogv1alpha1.RootFolderList
	require.NoError(t, c.List(ctx, &folders, client.InNamespace(tj.Namespace)))
	tk, err := BuildTask(tj, &tp, &mf, folders.Items, 1, tp.Spec.Hardware)
	if err != nil {
		return Outcome{Code: ExitInvalidSource, Err: err}
	}
	return Process(ctx, tk, o)
}

// process is processWith under the fixture's own Options.
func (f *fixture) process(t *testing.T, c client.Client) Outcome {
	t.Helper()
	return f.processWith(t, c, f.options())
}

// binEntries lists everything in the recycle bin, relative to it.
func (f *fixture) binEntries(t *testing.T) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(f.bin, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(f.bin, p)
			out = append(out, rel)
		}
		return nil
	})
	return out
}

func (f *fixture) partFiles(t *testing.T) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(filepath.Dir(f.local), "*.part.*"))
	require.NoError(t, err)
	return m
}

func (f *fixture) requireSourceUntouched(t *testing.T) {
	t.Helper()
	got, err := os.ReadFile(f.local)
	require.NoError(t, err, "the source must still exist")
	require.True(t, bytes.Equal(f.original, got), "the source must be byte-identical")
	assert.Empty(t, f.partFiles(t), "no output may be left beside the source")
	assert.Empty(t, f.binEntries(t), "nothing may have been recycled")
}

func videoCodec(t *testing.T, path string) (codec, tag string) {
	t.Helper()
	mi, raw, err := mediainfo.Probe(context.Background(), path)
	require.NoError(t, err)
	return mi.VideoCodec, formatTag(raw, "CLUSTARR_PROFILE")
}

// The happy path, end to end: a real encode lands the verified output at
// the source path, the original sits in the recycle bin, the seeding link
// under /data/torrents is untouched, and status carries the worker's three
// fields without disturbing the controller's.
func TestRunTranscodesVerifiesAndSwapsOverTheSource(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	f := newFixture(t, c)

	out := f.process(t, c)
	require.NoError(t, out.Err)
	require.Equal(t, ExitOK, out.Code)

	codec, tag := videoCodec(t, f.local)
	assert.Equal(t, "hevc", codec, "the source path must now hold the transcode")
	assert.Equal(t, f.profileName+"@"+f.profileHash, tag)
	assert.Empty(t, f.partFiles(t), "the .part must have been renamed, not copied")

	entries := f.binEntries(t)
	require.Len(t, entries, 1, "the original must be in the recycle bin")
	assert.Equal(t, filepath.Join(time.Now().UTC().Format("2006-01-02"), "Film.2020.1080p.mkv"), entries[0])
	recycled, err := os.ReadFile(filepath.Join(f.bin, entries[0]))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(f.original, recycled), "the recycled file must be the original, byte for byte")

	seed, err := os.ReadFile(f.seed)
	require.NoError(t, err, "the seeding copy must survive")
	assert.True(t, bytes.Equal(f.original, seed), "the seeding copy must be untouched (§6.4)")

	require.NotNil(t, out.Result)
	assert.Equal(t, f.logical, out.Result.OutputPath, "result carries the logical path, not this pod's mount")
	st, err := os.Stat(f.local)
	require.NoError(t, err)
	assert.Equal(t, st.Size(), out.Result.OutputSizeBytes)
	assert.Equal(t, sizePercent(st.Size(), int64(len(f.original))), out.Result.OutputToSourcePercent)
	require.NotNil(t, out.Result.MediaInfo)
	assert.Equal(t, "hevc", out.Result.MediaInfo.VideoCodec)

	// Process makes no Kubernetes client of its own (spec §9): the
	// TranscodeJob's controller-owned fields, and its managedFields, are
	// exactly as createJob left them. squasharr alone writes status, from
	// the events Serve publishes (app/squash/controller/transcodejob).
	tj := f.get(t, c)
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseRunning, tj.Status.Phase)
	require.NotNil(t, tj.Status.Plan)
	assert.Equal(t, "libx265", tj.Status.Plan.Encoder)
	assert.Nil(t, tj.Status.Result, "Process writes no status; squasharr does, from Serve's events")
	assert.Nil(t, tj.Status.Progress)
}

// R2/R4: a failed verification exits 4 and leaves the source exactly as it
// was -- not recycled, not replaced, no output left beside it.
func TestRunExitsFourAndLeavesTheSourceUntouchedWhenVerificationFails(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	f := newFixture(t, c)

	o := f.options()
	o.Verifier = failingVerifier{real: transcode.NewVerifier(ffprobeBin)}
	out := f.processWith(t, c, o)
	require.Error(t, out.Err)
	require.Equal(t, ExitVerifyFailed, out.Code)
	assert.Contains(t, out.Err.Error(), "stream count")

	f.requireSourceUntouched(t)
	seed, err := os.ReadFile(f.seed)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(f.original, seed))
	assert.Nil(t, out.Result, "a failed job has no result")
}

// failingVerifier runs the real verifier against the real output, then
// reports a problem, so the worker's handling of a genuine Report is what
// is exercised.
type failingVerifier struct{ real transcode.Verifier }

func (v failingVerifier) Verify(ctx context.Context, src, dst string, exp transcode.Expectation) (*transcode.Report, error) {
	r, err := v.real.Verify(ctx, src, dst, exp)
	if err != nil {
		return nil, err
	}
	r.OK = false
	r.Problems = append(r.Problems, "stream count 1, want 2")
	return r, nil
}

// R3: a source whose probe hash does not match the plan exits 3 BEFORE
// ffmpeg ever runs. ffmpeg is a wrapper that leaves a marker when invoked,
// so "before" is proven rather than inferred from the output's absence.
func TestRunExitsThreeBeforeRunningFFmpegWhenTheSourceChanged(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	f := newFixture(t, c)

	// The job was planned from a different file than the one now on disk.
	require.NoError(t, c.Delete(context.Background(), f.get(t, c)))
	f.createJob(t, c, "0123456789abcdef0123456789abcdef01234567")

	marker := filepath.Join(t.TempDir(), "ffmpeg-ran")
	wrapper := filepath.Join(t.TempDir(), "ffmpeg")
	require.NoError(t, os.WriteFile(wrapper,
		[]byte("#!/bin/sh\ntouch '"+marker+"'\nexec "+ffmpegBin+" \"$@\"\n"), 0o755))

	o := f.options()
	o.FFmpegPath = wrapper
	out := f.processWith(t, c, o)
	require.Error(t, out.Err)
	require.Equal(t, ExitInvalidSource, out.Code)
	assert.Contains(t, out.Err.Error(), "changed since it was planned")
	assert.Equal(t, task.ReasonSourceChanged, out.Reason)

	_, statErr := os.Stat(marker)
	assert.True(t, os.IsNotExist(statErr), "ffmpeg must never have been invoked")
	f.requireSourceUntouched(t)
}

// A source edited after BuildTask already planned it -- as opposed to a
// TranscodeJob created with a stale spec.sourceProbeHash, above -- is the
// same failure by a different route, and Process must report it through
// Outcome.Reason so a redispatching pool can act on it without reparsing the
// message (spec §18.1, §18.3).
func TestProcessReportsSourceChangedWhenTheSourceIsEditedAfterPlanning(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	ctx := context.Background()
	f := newFixture(t, c)

	tj := f.get(t, c)
	var tp transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: tj.Spec.ProfileRef}, &tp))
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: tj.Namespace, Name: tj.Spec.MediaFileRef}, &mf))
	var folders catalogv1alpha1.RootFolderList
	require.NoError(t, c.List(ctx, &folders, client.InNamespace(tj.Namespace)))
	tk, err := BuildTask(tj, &tp, &mf, folders.Items, 1, tp.Spec.Hardware)
	require.NoError(t, err)

	// The source is edited (different size, so a different ProbeHash) after
	// the task was already built from it.
	require.NoError(t, os.WriteFile(f.local, append(append([]byte{}, f.original...), 0), 0o644))

	out := Process(ctx, tk, f.options())
	require.Error(t, out.Err)
	assert.Equal(t, ExitInvalidSource, out.Code)
	assert.Equal(t, task.ReasonSourceChanged, out.Reason)
	assert.Empty(t, f.partFiles(t), "no output may be left beside the source")
	assert.Empty(t, f.binEntries(t), "nothing may have been recycled")
}

// The dangerous crash: the swap completed but the pod died before the
// result was recorded. Process itself never touches Kubernetes any more --
// squasharr does that, from Serve's events -- so what this
// exercises is purely file-system-level: a retry of the same task finds a
// source whose probe hash no longer matches -- which would be exit 3, a
// permanently failed task for a transcode that in fact succeeded -- unless
// it recognises its own output by the CLUSTARR_PROFILE tag. It must, and
// must not transcode again.
func TestRunAfterACrashPostSwapRecordsTheResultWithoutTranscodingAgain(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	f := newFixture(t, c)

	out := f.process(t, c)
	require.NoError(t, out.Err)
	require.Equal(t, ExitOK, out.Code)
	swapped, err := os.ReadFile(f.local)
	require.NoError(t, err)

	// The retry: same TranscodeJob, a wrapped ffmpeg that would leave a
	// marker if it ran.
	marker := filepath.Join(t.TempDir(), "ffmpeg-ran")
	wrapper := filepath.Join(t.TempDir(), "ffmpeg")
	require.NoError(t, os.WriteFile(wrapper,
		[]byte("#!/bin/sh\ntouch '"+marker+"'\nexec "+ffmpegBin+" \"$@\"\n"), 0o755))
	o := f.options()
	o.FFmpegPath = wrapper

	out2 := f.processWith(t, c, o)
	require.NoError(t, out2.Err)
	require.Equal(t, ExitOK, out2.Code)

	_, statErr := os.Stat(marker)
	assert.True(t, os.IsNotExist(statErr), "the retry must not transcode again")
	now, err := os.ReadFile(f.local)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(swapped, now), "the swapped-in output must be left as it is")
	assert.Len(t, f.binEntries(t), 1, "and nothing recycled a second time")

	require.NotNil(t, out2.Result)
	assert.Equal(t, f.logical, out2.Result.OutputPath)
	assert.Equal(t, int64(len(swapped)), out2.Result.OutputSizeBytes)
}

// Classification of the failure paths that need no encode. Each case is
// the permanent-or-not decision a caller acts on (podFailurePolicy today;
// task.Outcome/Reason once Task 6 lands).
//
// Two cases from before Process existed are gone rather than ported: a
// missing TranscodeJob and an unhashed TranscodeProfile are now caught by
// squasharr BEFORE it can even build a task.Task -- dispatch reads the job
// and refuses a profile with no status.hash
// (app/squash/controller/transcodejob).
// Process, given a task, no longer has Kubernetes objects to fail a Get
// against.
func TestRunClassifiesInputFailures(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	ctx := context.Background()

	t.Run("missing source file is permanent", func(t *testing.T) {
		f := newFixture(t, c)
		require.NoError(t, os.Remove(f.local))
		out := f.process(t, c)
		assert.Equal(t, ExitInvalidSource, out.Code)
	})

	t.Run("a source under no RootFolder is never touched", func(t *testing.T) {
		f := newFixture(t, c)
		var rf catalogv1alpha1.RootFolder
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "movies"}, &rf))
		require.NoError(t, c.Delete(ctx, &rf))
		out := f.process(t, c)
		assert.Equal(t, ExitInvalidSource, out.Code)
		assert.ErrorIs(t, out.Err, ErrNoRootFolder, "BuildTask refuses before Process ever runs")
		f.requireSourceUntouched(t)
	})

	t.Run("an output above maxOutputToSourcePercent fails verification", func(t *testing.T) {
		f := newFixture(t, c)
		var tp transcodev1alpha1.TranscodeProfile
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: f.profileName}, &tp))
		tp.Spec.Policy.MaxOutputToSourcePercent = ptr.To[int32](1)
		require.NoError(t, c.Update(ctx, &tp))
		out := f.process(t, c)
		assert.Equal(t, ExitVerifyFailed, out.Code)
		require.Error(t, out.Err)
		assert.Contains(t, out.Err.Error(), "maxOutputToSourcePercent")
		f.requireSourceUntouched(t)
	})
}

// createProfileFromYAML creates a TranscodeProfile the way kubectl does: from
// YAML, through an unstructured object, so every field the manifest leaves
// out is ABSENT on the wire and the apiserver applies its kubebuilder
// default. A typed create cannot show a default on a struct-valued field
// (encoding/json always sends a struct, so the field is present) and only
// happens to show one on an omitempty scalar; this is the honest shape of
// "what an operator who wrote the minimum gets".
func createProfileFromYAML(t *testing.T, c client.Client, name, spec string) error {
	t.Helper()
	var obj map[string]any
	require.NoError(t, yaml.Unmarshal([]byte("apiVersion: transcode.clustarr.io/v1alpha1\n"+
		"kind: TranscodeProfile\n"+
		"metadata:\n  name: "+name+"\n"+
		"spec:\n"+spec), &obj))
	return c.Create(context.Background(), &unstructured.Unstructured{Object: obj})
}

// The CRD default for policy.maxOutputToSourcePercent was 1 -- "fail any
// output larger than 1% of its source" -- so under a profile written with
// the minimum, every real transcode exited 4. E-3's tests passed only
// because they set the field. This one leaves it out, as an operator would,
// and transcodes a source that is realistically bloated next to HEVC (a
// near-lossless H.264 encode, the shape of a remux). The output must land.
func TestRunUnderTheCRDDefaultOutputLimitSwapsANormalTranscode(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	ctx := context.Background()

	f := newFixtureWith(t, c, fixtureOptions{
		videoArgs: []string{"-c:v", "libx264", "-preset", "ultrafast", "-crf", "1", "-pix_fmt", "yuv420p"},
		createProfile: func(t *testing.T, c client.Client, name string) {
			// minDuration is the one policy override: its default (1m) would
			// skip a two-second clip. maxOutputToSourcePercent is absent.
			require.NoError(t, createProfileFromYAML(t, c, name,
				"  video:\n    preset: ultrafast\n  policy:\n    minDuration: 0s\n"))
		},
	})

	var tp transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: f.profileName}, &tp))
	require.Equal(t, ptr.To[int32](100), tp.Spec.Policy.MaxOutputToSourcePercent,
		"the apiserver's default for policy.maxOutputToSourcePercent: an output no bigger than its source")
	require.True(t, ReplaceSource(tp.Spec.Policy))
	require.True(t, RecycleBin(tp.Spec.Policy))

	out := f.process(t, c)
	require.NoError(t, out.Err)
	require.Equal(t, ExitOK, out.Code)

	codec, _ := videoCodec(t, f.local)
	assert.Equal(t, "hevc", codec, "the verified output must be at the source path")
	require.NotNil(t, out.Result)
	assert.Positive(t, out.Result.OutputToSourcePercent)
	assert.LessOrEqual(t, out.Result.OutputToSourcePercent, int32(100))
	assert.Len(t, f.binEntries(t), 1, "the default recycles the original")
}

// policy.recycleBin=false is expressible now that it is a pointer, and the
// swap honours it: the output replaces the source, nothing enters the bin,
// and the seeding hard link still holds the original bytes.
func TestRunWithRecycleBinOffSwapsWithoutRecycling(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	ctx := context.Background()
	f := newFixture(t, c)

	var tp transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: f.profileName}, &tp))
	tp.Spec.Policy.RecycleBin = ptr.To(false)
	require.NoError(t, c.Update(ctx, &tp))
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: f.profileName}, &tp))
	require.NotNil(t, tp.Spec.Policy.RecycleBin, "a typed false must survive the round trip, not be re-defaulted")
	require.False(t, *tp.Spec.Policy.RecycleBin)

	out := f.process(t, c)
	require.NoError(t, out.Err)
	require.Equal(t, ExitOK, out.Code)

	codec, _ := videoCodec(t, f.local)
	assert.Equal(t, "hevc", codec)
	assert.Empty(t, f.binEntries(t), "recycleBin=false must not link the original into the bin")
	seed, err := os.ReadFile(f.seed)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(f.original, seed), "the seeding copy is a separate link and survives (§6.4)")
}

// replaceSource=false is a v1 spec value (gap-fix ruling R-11), so the
// apiserver admits it; the CEL rule that refused it is gone, and the worker
// honours it (TestRunWithReplaceSourceFalseKeepsTheSource).
func TestTheAPIAdmitsReplaceSourceFalse(t *testing.T) {
	c := requireCluster(t)
	require.NoError(t, createProfileFromYAML(t, c, "replace-source-false", "  policy:\n    replaceSource: false\n"))
	require.NoError(t, createProfileFromYAML(t, c, "replace-source-true", "  policy:\n    replaceSource: true\n"))
}

// --- gap-fix ruling R-11: output location, container change, kept source ---

// A container change (an .mp4 source under the default mkv profile) is
// transcoded to <stem>.mkv beside the source; once that is in place the
// source is retired to the recycle bin, the seeding link is untouched, and
// status.result names the new path for catalogarr to take spec.path from.
func TestRunChangesTheContainerAndRetiresTheSource(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	f := newFixtureWith(t, c, fixtureOptions{fileName: "Film.2020.1080p.mp4"})
	wantLogical := "/data/media/movies/Film (2020)/Film.2020.1080p.mkv"
	wantLocal := filepath.Join(f.dataDir, "media/movies/Film (2020)/Film.2020.1080p.mkv")

	out := f.process(t, c)
	require.NoError(t, out.Err)
	require.Equal(t, ExitOK, out.Code)

	codec, tag := videoCodec(t, wantLocal)
	assert.Equal(t, "hevc", codec, "the output must be at <stem>.mkv")
	assert.Equal(t, f.profileName+"@"+f.profileHash, tag)
	mi, _, err := mediainfo.Probe(context.Background(), wantLocal)
	require.NoError(t, err)
	assert.Equal(t, "mkv", mi.Container, "mkv data behind an .mkv name, not the .mp4 one")
	_, err = os.Stat(f.local)
	assert.ErrorIs(t, err, os.ErrNotExist, "the .mp4 source must be retired from the library")
	entries := f.binEntries(t)
	require.Len(t, entries, 1)
	recycled, err := os.ReadFile(filepath.Join(f.bin, entries[0]))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(f.original, recycled), "the recycled file is the original")
	seed, err := os.ReadFile(f.seed)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(f.original, seed), "the seeding link is untouched (§6.4)")
	assert.Empty(t, f.partFiles(t))

	require.NotNil(t, out.Result)
	assert.Equal(t, wantLogical, out.Result.OutputPath)
}

// replaceSource=false writes the output under the multiple-version name
// beside the source and leaves the source exactly as it was.
func TestRunWithReplaceSourceFalseKeepsTheSource(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	f := newFixtureWith(t, c, fixtureOptions{mutateProfile: func(tp *transcodev1alpha1.TranscodeProfile) {
		tp.Spec.Policy.ReplaceSource = ptr.To(false)
	}})
	name := "Film.2020.1080p - " + f.profileName + ".mkv"
	outLocal := filepath.Join(f.dataDir, "media/movies/Film (2020)", name)

	out := f.process(t, c)
	require.NoError(t, out.Err)
	require.Equal(t, ExitOK, out.Code)

	codec, tag := videoCodec(t, outLocal)
	assert.Equal(t, "hevc", codec)
	assert.Equal(t, f.profileName+"@"+f.profileHash, tag)
	f.requireSourceUntouched(t)
	require.NotNil(t, out.Result)
	assert.Equal(t, "/data/media/movies/Film (2020)/"+name, out.Result.OutputPath)
}

// plantOutput puts a small mkv at path, tagged CLUSTARR_PROFILE=tag when
// tag is non-empty.
func plantOutput(t *testing.T, path, tag string) []byte {
	t.Helper()
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x120:rate=24:duration=1",
		"-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "log-level=none", "-pix_fmt", "yuv420p10le",
	}
	if tag != "" {
		args = append(args, "-metadata", "CLUSTARR_PROFILE="+tag)
	}
	out, err := exec.Command(ffmpegBin, append(args, "-f", "matroska", path)...).CombinedOutput()
	require.NoError(t, err, string(out))
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}

// A crash after the output took its new name but before the source was
// retired leaves both. The retry must not transcode again: it finds this
// profile's tag on the output, retires the source, and records the result.
func TestRunAfterACrashPostPlaceRetiresTheSourceWithoutTranscoding(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	f := newFixtureWith(t, c, fixtureOptions{fileName: "Film.2020.1080p.mp4"})
	outLocal := filepath.Join(f.dataDir, "media/movies/Film (2020)/Film.2020.1080p.mkv")
	placed := plantOutput(t, outLocal, f.profileName+"@"+f.profileHash)

	out := f.process(t, c)
	require.NoError(t, out.Err)
	require.Equal(t, ExitOK, out.Code)

	got, err := os.ReadFile(outLocal)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(placed, got), "the placed output must not be encoded again")
	_, err = os.Stat(f.local)
	assert.ErrorIs(t, err, os.ErrNotExist, "the retry must finish retiring the source")
	assert.Len(t, f.binEntries(t), 1)
	require.NotNil(t, out.Result)
	assert.Equal(t, "/data/media/movies/Film (2020)/Film.2020.1080p.mkv", out.Result.OutputPath)
}

// A file already at the output path that is NOT this transcode -- no tag --
// is never overwritten: the job fails outright (exit 3) and both files stay.
func TestRunRefusesToOverwriteAnUnrelatedFileAtTheOutputPath(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	f := newFixtureWith(t, c, fixtureOptions{fileName: "Film.2020.1080p.mp4"})
	outLocal := filepath.Join(f.dataDir, "media/movies/Film (2020)/Film.2020.1080p.mkv")
	theirs := plantOutput(t, outLocal, "")

	out := f.process(t, c)
	require.Error(t, out.Err)
	require.Equal(t, ExitInvalidSource, out.Code)

	got, err := os.ReadFile(outLocal)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(theirs, got), "the unrelated file must be untouched")
	f.requireSourceUntouched(t)
}

// An explicit spec.outputPath, in a folder that does not exist yet under the
// same RootFolder, is honoured: the folder is created and the output lands
// there, and the source is retired as for any replacing transcode.
func TestRunWritesAnExplicitOutputPath(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	ctx := context.Background()
	f := newFixture(t, c)
	tj := f.get(t, c)
	require.NoError(t, c.Delete(ctx, tj))
	logicalOut := "/data/media/movies/Film (2020) [hevc]/Film (2020).mkv"
	require.NoError(t, c.Create(ctx, &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: f.job, Namespace: f.ns},
		Spec: transcodev1alpha1.TranscodeJobSpec{
			MediaFileRef: "film-2020", ProfileRef: f.profileName,
			SourcePath: f.logical, SourceProbeHash: f.probeHash, OutputPath: ptr.To(logicalOut),
		},
	}))

	out := f.process(t, c)
	require.NoError(t, out.Err)
	require.Equal(t, ExitOK, out.Code)

	codec, _ := videoCodec(t, filepath.Join(f.dataDir, "media/movies/Film (2020) [hevc]/Film (2020).mkv"))
	assert.Equal(t, "hevc", codec)
	_, err := os.Stat(f.local)
	assert.ErrorIs(t, err, os.ErrNotExist, "replaceSource=true retires the source")
	require.NotNil(t, out.Result)
	assert.Equal(t, logicalOut, out.Result.OutputPath)

	// Outside every RootFolder it is refused before any work: BuildTask
	// itself refuses (ErrNoRootFolder), before Process is ever called.
	f2 := newFixture(t, c)
	require.NoError(t, c.Delete(ctx, f2.get(t, c)))
	require.NoError(t, c.Create(ctx, &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: f2.job, Namespace: f2.ns},
		Spec: transcodev1alpha1.TranscodeJobSpec{
			MediaFileRef: "film-2020", ProfileRef: f2.profileName,
			SourcePath: f2.logical, SourceProbeHash: f2.probeHash, OutputPath: ptr.To("/data/elsewhere/Film.mkv"),
		},
	}))
	out2 := f2.process(t, c)
	require.Error(t, out2.Err)
	assert.Equal(t, ExitInvalidSource, out2.Code)
	f2.requireSourceUntouched(t)
}
