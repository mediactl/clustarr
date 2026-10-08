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

package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	batchac "k8s.io/client-go/applyconfigurations/batch/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/app/import/worker/probe/probetest"
	"github.com/mediactl/clustarr/app/remediation/itemtest"
	"github.com/mediactl/clustarr/app/transcode/grafttask"
	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/test/starttest"
)

// graftFixture is one movie with a Japanese-only file, a dual-audio profile
// that grafts, and an imported English donor -- an audioDonor entry in the
// movie's status.downloads whose execute record in clustarr-imports names
// the reduced audio (ADR-0019 F7.1) -- under the full manager registration
// (register) on a JetStream bus, with the import domain's real probe worker
// answering through a fake prober.
type graftFixture struct {
	c       client.Client
	bus     *natsbus.Bus
	mf      *catalogv1alpha1.MediaFile
	movie   *catalogv1alpha1.Movie
	path    string
	grafted atomic.Bool
	hevc    atomic.Bool // the probe reports hevc: a later re-encode of the file
}

func startGraftFixture(t *testing.T, name string) *graftFixture {
	t.Helper()
	env := starttest.StartEnv(t)
	ctx := t.Context()
	o := testOptions()
	o.Autoscale, o.LeaderElect, o.BusSingleNode = false, false, true
	bus, nc, err := busconn.Connect(starttest.EmbeddedNATS(t), "manager")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close(); nc.Close() })
	require.NoError(t, busconn.EnsureTopology(ctx, bus, o.BusTopology()))
	f := &graftFixture{bus: bus}
	probetest.Start(t, bus, probetest.ProberFunc(func(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
		audio := []commonv1.AudioStream{{Index: 1, Codec: "aac", Channels: 2, Language: "jpn"}}
		if f.grafted.Load() {
			audio = append(audio, commonv1.AudioStream{Index: 2, Codec: "aac", Channels: 2, Language: "eng"})
		}
		codec := "h264"
		if f.hevc.Load() {
			codec = "hevc"
		}
		return &commonv1.MediaInfo{Container: "matroska", VideoCodec: codec, Width: 1920, Height: 1080, Audio: audio}, &mediainfo.Raw{}, nil
	}))

	mgr, err := ctrl.NewManager(env.Config, managerOptions(o))
	require.NoError(t, err)
	_, err = register(mgr, bus, o)
	require.NoError(t, err)
	mctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(mctx) }()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Error("the manager did not stop within 30s")
		}
	})
	f.c, err = client.New(env.Config, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)

	root := mediaTempDir(t)
	f.path = filepath.Join(root, "Perfect Blue (1997)", "Perfect Blue (1997).mkv")
	require.NoError(t, os.MkdirAll(filepath.Dir(f.path), 0o755))
	require.NoError(t, os.WriteFile(f.path, []byte("japanese-only bytes"), 0o644))
	require.NoError(t, f.c.Create(ctx, &catalogv1alpha1.RootFolder{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: catalogv1alpha1.RootFolderSpec{Path: root, Kind: catalogv1alpha1.RootFolderKindMovie}}))
	require.NoError(t, f.c.Create(ctx, &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: catalogv1alpha1.QualityProfileSpec{MediaKind: catalogv1alpha1.ProfileMediaKindVideo, Cutoff: "hd",
			Tiers: []catalogv1alpha1.Tier{{Name: "hd", Qualities: []string{"Bluray-1080p"}}},
			Audio: &catalogv1alpha1.AudioPolicy{Languages: []string{"en", "original"}, Graft: true, Default: "en"}}}))
	f.movie = &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: catalogv1alpha1.MovieSpec{TmdbID: 10494, QualityProfileRef: name, RootFolderRef: name, Monitored: new(false)}}
	require.NoError(t, f.c.Create(ctx, f.movie))
	_, err = k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogMetadata, catalogac.Movie(name, "default").WithStatus(catalogac.MovieStatus().
		WithMetadata(catalogac.MovieMetadata().WithTitle("Perfect Blue").WithYear(1997).WithOriginalLanguage("ja").WithRefreshedAt(metav1.Now()))))
	require.NoError(t, err)
	require.NoError(t, f.c.Get(ctx, client.ObjectKeyFromObject(f.movie), f.movie))

	// The donor: an audioDonor entry, imported, whose execute record names
	// the reduced audio under .clustarr/donors (§2.6).
	donor := filepath.Join(root, ".clustarr", "donors", string(f.movie.UID), name+".mka")
	entry := itemtest.Entry(name+"-donor", commonv1.DownloadPhaseImported)
	entry.Purpose = commonv1.DownloadPurposeAudioDonor
	entry.Release.Title = "Perfect.Blue.1997.DVDRip.DUAL-GRP"
	imported := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	entry.Import = &catalogv1alpha1.DownloadImportSummary{Phase: catalogv1alpha1.ImportPhaseImported, ImportedAt: &imported,
		Dispatch: catalogv1alpha1.Dispatch{Seq: 1, AnsweredSeq: 1, DispatchedAt: imported}}
	w := records.NewWriter(bus.KV(events.BucketImports), agentrecords.Imports(), "import-agent", "test")
	_, err = w.Write(ctx, agentrecords.ImportKey(entry.UID, schema.ImportSubExecute), &schema.ImportRecord{
		RecordHeader: schema.RecordHeader{Item: &schema.ItemRef{Kind: "Movie", Namespace: "default", Name: name, UID: string(f.movie.UID)},
			Sub: schema.ImportSubExecute, Seq: 1, State: records.StateAnswered},
		Entry:   schema.EntryRef{ID: entry.ID, UID: entry.UID},
		Execute: &schema.ImportExecution{DonorPath: donor},
	})
	require.NoError(t, err)
	require.NoError(t, itemtest.SeedEntries(ctx, f.c, f.movie, entry))

	f.mf = &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: name + "-file", Namespace: "default"},
		Spec: catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}, Path: f.path}}
	require.NoError(t, f.c.Create(ctx, f.mf))
	return f
}

// mediaTempDir is a fresh directory under the test media root: the
// RootFolder CRD admits only paths under /data/media/, which `make test`
// creates (CLUSTARR_TEST_MEDIA_ROOT overrides it). Its absence is a named
// skip, as in the import suites: a missing prerequisite is not a defect.
func mediaTempDir(t *testing.T) string {
	t.Helper()
	prefix := os.Getenv("CLUSTARR_TEST_MEDIA_ROOT")
	if prefix == "" {
		prefix = "/data/media"
	}
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Skipf("%s is not creatable (%v): RootFolder.spec.path must start with /data/media/; run `make test` "+
			"or set CLUSTARR_TEST_MEDIA_ROOT to a writable directory under it", prefix, err)
	}
	dir, err := os.MkdirTemp(prefix, "clustarr-manager-graft-")
	if err != nil {
		t.Skipf("%s is not writable (%v): RootFolder.spec.path must start with /data/media/; run `make test` "+
			"or set CLUSTARR_TEST_MEDIA_ROOT to a writable directory under it", prefix, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func (f *graftFixture) file(t *testing.T) catalogv1alpha1.MediaFile {
	t.Helper()
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, f.c.Get(t.Context(), client.ObjectKeyFromObject(f.mf), &mf))
	return mf
}

// graftJob waits for the file's graft Job and returns it with its task.
func (f *graftFixture) graftJob(t *testing.T) (batchv1.Job, grafttask.Task) {
	t.Helper()
	var job batchv1.Job
	require.Eventually(t, func() bool {
		var jobs batchv1.JobList
		if f.c.List(t.Context(), &jobs, client.InNamespace("default"),
			client.MatchingLabels{grafttask.LabelGraftOf: string(f.file(t).UID)}) != nil || len(jobs.Items) != 1 {
			return false
		}
		job = jobs.Items[0]
		return true
	}, 2*time.Minute, 250*time.Millisecond, "no graft Job: the item's donor, the graft block or the slot grant never came\nmovie: %s\nfile: %s",
		f.debugMovie(t), f.debugFile(t))
	args := job.Spec.Template.Spec.Containers[0].Args
	i := slices.Index(args, "--graft-task")
	require.GreaterOrEqual(t, i, 0)
	var tk grafttask.Task
	require.NoError(t, json.Unmarshal([]byte(args[i+1]), &tk))
	return job, tk
}

func (f *graftFixture) debugMovie(t *testing.T) string {
	var m catalogv1alpha1.Movie
	_ = f.c.Get(context.Background(), client.ObjectKeyFromObject(f.movie), &m)
	b, _ := json.Marshal(m.Status)
	return string(b)
}

func (f *graftFixture) debugFile(t *testing.T) string {
	var mf catalogv1alpha1.MediaFile
	_ = f.c.Get(context.Background(), client.ObjectKeyFromObject(f.mf), &mf)
	b, _ := json.Marshal(mf.Status)
	return string(b)
}

// TestAStandaloneGraftIsDispatchedAndItsAnswerIncorporated (§4.12, §5.13,
// §3.10): the file waits for a slot, is granted one, goes Queued with its
// Job, the pod answers Grafted in clustarr-grafts, the block reads Swapping
// until the grafted bytes' probe lands, then Succeeded; status.graftTag
// takes the tag, the grafted original's size and mtime become the catalog's
// while spec.original stays, the Job is deleted, and the movie reads done.
func TestAStandaloneGraftIsDispatchedAndItsAnswerIncorporated(t *testing.T) {
	f := startGraftFixture(t, "graft-standalone")
	ctx := t.Context()
	job, tk := f.graftJob(t)
	mf := f.file(t)
	require.NotNil(t, mf.Status.Graft)
	require.NotNil(t, mf.Status.Graft.Dispatch)
	assert.Equal(t, catalogv1alpha1.GraftPhaseQueued, mf.Status.Graft.Phase)
	assert.Equal(t, mf.Status.Graft.Dispatch.Seq, tk.Seq)
	assert.Equal(t, mf.Status.Graft.JobName, job.Name)
	assert.Equal(t, string(mf.UID), tk.File.UID)
	assert.Equal(t, "en", tk.Language)

	res, err := grafttask.Serve(ctx, grafttask.NewAnswerer(f.bus, "graft-pod-0"), tk, func(context.Context, grafttask.Task) grafttask.Result {
		f.appendDub(t)
		return grafttask.Result{Phase: grafttask.PhaseSucceeded, Reason: grafttask.ReasonGrafted, GraftTag: "tag-standalone"}
	})
	require.NoError(t, err)
	assert.Equal(t, grafttask.ReasonGrafted, res.Reason)

	require.Eventually(t, func() bool {
		mf = f.file(t)
		return mf.Status.Graft != nil && mf.Status.Graft.Phase == catalogv1alpha1.GraftPhaseSucceeded
	}, 2*time.Minute, 250*time.Millisecond, "the graft never succeeded")
	assert.Equal(t, "tag-standalone", mf.Status.GraftTag)
	require.NotNil(t, mf.Status.GraftedAt)
	assert.Equal(t, mf.Status.Graft.Dispatch.Seq, mf.Status.Graft.Dispatch.AnsweredSeq)
	assert.True(t, mf.Spec.Original == nil || *mf.Spec.Original, "a graft is not a transcode")
	st, err := os.Stat(f.path)
	require.NoError(t, err)
	assert.Equal(t, st.Size(), mf.Spec.SizeBytes, "the grafted original's size is the catalog's")
	assert.True(t, ownsSpec(mf, `"f:sizeBytes"`) && ownsSpec(mf, `"f:modTime"`),
		"the catalog owns spec.sizeBytes and spec.modTime after the graft (§3.10)")
	require.Eventually(t, func() bool {
		var j batchv1.Job
		return apierrors.IsNotFound(f.c.Get(ctx, client.ObjectKeyFromObject(&job), &j))
	}, time.Minute, 250*time.Millisecond, "the answered graft's Job was not deleted")
	require.Eventually(t, func() bool {
		var m catalogv1alpha1.Movie
		return f.c.Get(ctx, client.ObjectKeyFromObject(f.movie), &m) == nil && m.Status.Audio != nil && m.Status.Audio.Graft == "done"
	}, time.Minute, 250*time.Millisecond, "the movie never read the graft done")
}

// appendDub is the graft pod's work as the fixture sees it: the original
// gains the English track (the probe now reports it) and a new mtime.
func (f *graftFixture) appendDub(t *testing.T) {
	fh, err := os.OpenFile(f.path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = fh.WriteString(" + the english dub")
	require.NoError(t, err)
	require.NoError(t, fh.Close())
	later := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(f.path, later, later))
	f.grafted.Store(true)
}

// ownsSpec reports whether the catalog's main apply owns field of spec.
func ownsSpec(mf catalogv1alpha1.MediaFile, field string) bool {
	for _, e := range mf.ManagedFields {
		if e.Manager == k8s.ManagerCatalog.String() && e.Subresource == "" && e.FieldsV1 != nil &&
			strings.Contains(string(e.FieldsV1.Raw), field) {
			return true
		}
	}
	return false
}

// TestALostGraftJobIsWithdrawnAndFails (§4.12 "Job ... with no answer after
// graftJobGrace", through the loop): the Job failed three minutes ago and
// its pod never answered. The block fails JobLost, its dispatch is
// withdrawn, and so is the record, fencing a pod that wakes late.
func TestALostGraftJobIsWithdrawnAndFails(t *testing.T) {
	f := startGraftFixture(t, "graft-lost")
	ctx := t.Context()
	job, tk := f.graftJob(t)
	past := metav1.NewTime(time.Now().Add(-3 * time.Minute))
	// The apiserver validates Job status transitions: FailureTarget precedes Failed.
	require.NoError(t, f.c.Status().Apply(ctx, batchac.Job(job.Name, job.Namespace).WithStatus(batchac.JobStatus().
		WithStartTime(metav1.NewTime(past.Add(-time.Minute))).WithFailed(1).WithConditions(
		batchac.JobCondition().WithType(batchv1.JobFailureTarget).WithStatus(corev1.ConditionTrue).
			WithLastTransitionTime(past).WithReason("DeadlineExceeded"),
		batchac.JobCondition().WithType(batchv1.JobFailed).WithStatus(corev1.ConditionTrue).
			WithLastTransitionTime(past).WithReason("DeadlineExceeded").
			WithMessage("Job was active longer than specified deadline"))),
		client.FieldOwner("graft-envtest"), client.ForceOwnership))

	var mf catalogv1alpha1.MediaFile
	require.Eventually(t, func() bool {
		mf = f.file(t)
		return mf.Status.Graft != nil && mf.Status.Graft.Phase == catalogv1alpha1.GraftPhaseFailed
	}, time.Minute, 250*time.Millisecond, "the lost Job's graft never failed\nfile: %s", f.debugFile(t))
	assert.Equal(t, "JobLost", mf.Status.Graft.Reason)
	assert.Contains(t, mf.Status.Graft.Message, "deadline")
	require.NotNil(t, mf.Status.Graft.Dispatch)
	assert.True(t, mf.Status.Graft.Dispatch.Withdrawn)
	r, _, found, err := grafttask.NewRequester(f.bus).Get(ctx, events.RecordKey(string(mf.UID)))
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, tk.Seq, r.Seq)
	assert.Equal(t, records.StateWithdrawn, r.State, "the withdrawal fences a pod that wakes late")
}

// graftToSuccess runs the fixture's graft to Succeeded as
// TestAStandaloneGraftIsDispatchedAndItsAnswerIncorporated does.
func (f *graftFixture) graftToSuccess(t *testing.T) catalogv1alpha1.MediaFile {
	t.Helper()
	_, tk := f.graftJob(t)
	_, err := grafttask.Serve(t.Context(), grafttask.NewAnswerer(f.bus, "graft-pod-0"), tk, func(context.Context, grafttask.Task) grafttask.Result {
		f.appendDub(t)
		return grafttask.Result{Phase: grafttask.PhaseSucceeded, Reason: grafttask.ReasonGrafted, GraftTag: "tag-" + f.mf.Name}
	})
	require.NoError(t, err)
	var mf catalogv1alpha1.MediaFile
	require.Eventually(t, func() bool {
		mf = f.file(t)
		return mf.Status.Graft != nil && mf.Status.Graft.Phase == catalogv1alpha1.GraftPhaseSucceeded
	}, 2*time.Minute, 250*time.Millisecond, "the graft never succeeded")
	return mf
}

// TestAGraftedOriginalKeepsItsSpecWhenTheGraftBlockGoes (loop spec §3.10,
// §3.20): the main apply keys spec.path, sizeBytes and modTime on the flat
// status.graftTag, never on the status.graft block, so a grafted original
// whose block goes -- its profile stops grafting -- keeps the catalog's
// ownership of them through the next main apply, sent here because a probe
// moves the video-codec label. Keyed on the block, that apply would omit
// them and release them, and as optional fields they would leave spec.
func TestAGraftedOriginalKeepsItsSpecWhenTheGraftBlockGoes(t *testing.T) {
	f := startGraftFixture(t, "graft-goes")
	ctx := t.Context()
	f.graftToSuccess(t)

	var qp catalogv1alpha1.QualityProfile
	require.NoError(t, f.c.Get(ctx, client.ObjectKey{Name: "graft-goes"}, &qp))
	base := qp.DeepCopy()
	qp.Spec.Audio.Graft = false
	require.NoError(t, f.c.Patch(ctx, &qp, client.MergeFrom(base)))
	require.Eventually(t, func() bool { return f.file(t).Status.Graft == nil }, time.Minute, 250*time.Millisecond,
		"the graft block stayed after the profile stopped grafting")
	steady := f.file(t)
	require.NotEmpty(t, steady.Status.GraftTag, "status.graftTag outlives the block")
	for _, field := range []string{`"f:path"`, `"f:sizeBytes"`, `"f:modTime"`} {
		require.True(t, ownsSpec(steady, field), "the catalog owns spec %s before the re-probe", field)
	}

	f.hevc.Store(true)
	fh, err := os.OpenFile(f.path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = fh.WriteString(" + a re-encode elsewhere")
	require.NoError(t, err)
	require.NoError(t, fh.Close())
	later := time.Now().Add(4 * time.Second)
	require.NoError(t, os.Chtimes(f.path, later, later))
	touched := steady.DeepCopy()
	if touched.Annotations == nil {
		touched.Annotations = map[string]string{}
	}
	// The import scan's key: the loop re-stats and re-probes.
	touched.Annotations[mediafilespec.AnnotationObservedFingerprint] = "after-the-graft-block-went"
	require.NoError(t, f.c.Patch(ctx, touched, client.MergeFrom(&steady)))

	var mf catalogv1alpha1.MediaFile
	require.Eventually(t, func() bool {
		mf = f.file(t)
		return mf.Labels[catalogv1alpha1.LabelVideoCodec] == "hevc"
	}, 2*time.Minute, 250*time.Millisecond, "the new probe's codec never reached the labels: no main apply was sent")
	for _, field := range []string{`"f:path"`, `"f:sizeBytes"`, `"f:modTime"`} {
		assert.True(t, ownsSpec(mf, field), "the catalog released spec %s when the graft block went (§3.10)", field)
	}
	st, err := os.Stat(f.path)
	require.NoError(t, err)
	assert.Equal(t, st.Size(), mf.Spec.SizeBytes, "the re-stat's size reached spec")
}
