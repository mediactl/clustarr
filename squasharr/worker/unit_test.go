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
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/squasharr/task"
)

func TestLocalPathMapsLogicalDataPathsAndRefusesEverythingElse(t *testing.T) {
	cases := []struct {
		name, dataDir, in, want string
		wantErr                 bool
	}{
		{name: "identity in a pod", dataDir: "/data", in: "/data/media/movies/A (2020)/A.mkv", want: "/data/media/movies/A (2020)/A.mkv"},
		{name: "remapped", dataDir: "/tmp/x", in: "/data/media/movies/A.mkv", want: "/tmp/x/media/movies/A.mkv"},
		{name: "dot-dot cannot escape", dataDir: "/tmp/x", in: "/data/media/../../etc/passwd", wantErr: true},
		{name: "outside /data", dataDir: "/data", in: "/etc/passwd", wantErr: true},
		{name: "prefix is not containment", dataDir: "/data", in: "/database/x.mkv", wantErr: true},
		{name: "relative", dataDir: "/data", in: "media/x.mkv", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := localPath(tc.dataDir, tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRootFolderForPicksTheDeepestContainingFolderAndNeverAPrefixMatch(t *testing.T) {
	rf := func(name, path string) catalogv1alpha1.RootFolder {
		return catalogv1alpha1.RootFolder{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: catalogv1alpha1.RootFolderSpec{Path: path}}
	}
	folders := []catalogv1alpha1.RootFolder{
		rf("media", "/data/media"),
		rf("movies", "/data/media/movies"),
		rf("movies4k", "/data/media/movies4k"),
	}
	assert.Equal(t, "movies", rootFolderFor(folders, "/data/media/movies/A (2020)/A.mkv").Name)
	assert.Equal(t, "movies4k", rootFolderFor(folders, "/data/media/movies4k/A.mkv").Name)
	assert.Equal(t, "media", rootFolderFor(folders, "/data/media/tv/x.mkv").Name)
	assert.Nil(t, rootFolderFor(folders, "/data/torrents/A.mkv"), "a seeding copy is under no root folder")
	assert.Nil(t, rootFolderFor(folders[1:], "/data/media/movies"), "the folder itself is not inside itself")
}

// Every classification helper must produce the code its name promises,
// through wrapping, and anything unclassified must be retriable -- never 3
// or 4, which the Job treats as permanent.
func TestExitCodeClassification(t *testing.T) {
	assert.Equal(t, ExitOK, ExitCode(nil))
	assert.Equal(t, ExitRetriable, ExitCode(retriable("x")))
	assert.Equal(t, ExitInvalidSource, ExitCode(invalidSource("x")))
	assert.Equal(t, ExitVerifyFailed, ExitCode(verifyFailed("x")))
	assert.Equal(t, ExitInvalidSource, ExitCode(fmt.Errorf("wrapped: %w", invalidSource("x"))))
	assert.Equal(t, ExitRetriable, ExitCode(errors.New("unclassified")))
	assert.Equal(t, []int{0, 2, 3, 4}, []int{ExitOK, ExitRetriable, ExitInvalidSource, ExitVerifyFailed},
		"the codes are a contract with podFailurePolicy (R4); they do not move")
}

// The three constructors squasharr itself decides the reason for (spec
// §18.1, §18.3) must still classify to the same exit code every other
// failure of their kind does, and must carry the task.Reason a redispatching
// caller reads without reparsing the message.
func TestExitCodeClassifiesTheNamedReasons(t *testing.T) {
	assert.Equal(t, ExitInvalidSource, ExitCode(sourceChanged("x")))
	assert.Equal(t, ExitRetriable, ExitCode(gpuUnavailable("x")))
	assert.Equal(t, ExitRetriable, ExitCode(gpuEncodeFailed(errors.New("x"))))

	var f *failure
	require.ErrorAs(t, sourceChanged("x"), &f)
	assert.Equal(t, task.ReasonSourceChanged, f.reason)
	f = nil
	require.ErrorAs(t, gpuUnavailable("x"), &f)
	assert.Equal(t, task.ReasonGPUUnavailable, f.reason)
	f = nil
	require.ErrorAs(t, gpuEncodeFailed(errors.New("x")), &f)
	assert.Equal(t, task.ReasonGPUEncodeFailed, f.reason)

	// A plain retriable/invalidSource/verifyFailed carries no reason: only
	// squasharr's own decisions do.
	f = nil
	require.ErrorAs(t, retriable("x"), &f)
	assert.Empty(t, f.reason)
}

// ProfileSpec must carry every render-relevant field. Populate every field
// of the CRD spec with a non-zero value and require every leaf of the
// result to be non-zero: a field added to transcode.ProfileSpec later and
// forgotten here fails by name.
func TestProfileSpecCarriesEveryField(t *testing.T) {
	tune := "grain"
	spec := transcodev1alpha1.TranscodeProfileSpec{
		Container: transcodev1alpha1.ContainerMP4,
		Hardware:  transcodev1alpha1.HardwareNVIDIA,
		Video: transcodev1alpha1.VideoSpec{
			Codec: "hevc", PixelFormat: "yuv420p10le", Profile: "main10",
			CRF:    transcodev1alpha1.CRFTable{SD: 1, HD: 2, UHD: 3, HDROffset: ptr.To[int32](-1)},
			Preset: "slow", Tune: &tune, KeyintFactor: 10, BFrames: 8, Refs: 4, RCLookahead: 40, AQMode: 3,
			MaxRateKbps: ptr.To[int32](1), BufSizeKbps: ptr.To[int32](2),
			ExtraX265Params: map[string]string{"a": "b"},
			NVENC:           transcodev1alpha1.NVENCSpec{Preset: "p6", Tune: "hq", CQ: 24, Multipass: "fullres", BRefMode: "middle"},
			QSV:             transcodev1alpha1.QSVSpec{GlobalQuality: 22, Preset: "veryslow", LookAheadDepth: 40},
		},
		Audio: transcodev1alpha1.AudioSpec{
			Codec: "aac", BitratePerChannelKbps: 64, KeepOriginal: transcodev1alpha1.KeepOriginalAtmos,
			Languages: []string{"en"}, DropCommentary: ptr.To(true), StereoCompatTrack: true,
		},
		Subtitles: transcodev1alpha1.SubSpec{CopyText: ptr.To(true), CopyBitmap: ptr.To(true), CopyAttachments: ptr.To(true)},
		HDR:       transcodev1alpha1.HDRSpec{HDR10Plus: transcodev1alpha1.HDR10PlusDrop, DolbyVision: transcodev1alpha1.DolbyVisionReject},
		Policy: transcodev1alpha1.PolicySpec{
			SkipIfCompliant: ptr.To(true), RemuxOnlyWhenVideoCompliant: ptr.To(true), NeverTranscodeModifiers: []string{"remux"},
			MinDuration: &metav1.Duration{Duration: time.Minute}, MaxOutputToSourcePercent: ptr.To[int32](100),
			ReplaceSource: ptr.To(true), RecycleBin: ptr.To(true),
		},
		Verify:  transcodev1alpha1.VerifySpec{PacketCount: ptr.To(true), FullDecode: true, VMAFMinCentis: ptr.To[int32](9000)},
		Scratch: resource.MustParse("1Gi"),
	}
	got := ProfileSpec(spec, nil)
	assertNoZeroLeaf(t, reflect.ValueOf(got), "ProfileSpec")
	assert.Equal(t, transcode.HardwareNVIDIA, got.Hardware)

	cpu := transcodev1alpha1.HardwareCPU
	assert.Equal(t, transcode.HardwareCPU, ProfileSpec(spec, &cpu).Hardware, "TranscodeJob.spec.hardware overrides the profile")
}

// policy.replaceSource and policy.recycleBin are pointers so a Go client
// can say false; unset must still mean the CRD default, true, because a spec
// built in Go never passes through the apiserver's defaulting.
func TestPolicyPointersDefaultToTrue(t *testing.T) {
	var unset transcodev1alpha1.PolicySpec
	assert.True(t, ReplaceSource(unset))
	assert.True(t, RecycleBin(unset))
	assert.True(t, ProfileSpec(transcodev1alpha1.TranscodeProfileSpec{}, nil).Policy.ReplaceSource)
	assert.True(t, ProfileSpec(transcodev1alpha1.TranscodeProfileSpec{}, nil).Policy.RecycleBin)

	off := transcodev1alpha1.PolicySpec{ReplaceSource: ptr.To(false), RecycleBin: ptr.To(false)}
	assert.False(t, ReplaceSource(off))
	assert.False(t, RecycleBin(off))
	assert.False(t, ProfileSpec(transcodev1alpha1.TranscodeProfileSpec{Policy: off}, nil).Policy.RecycleBin)
}

// The defaults ProfileSpec applies to a nil pointer restate the CRD's; this
// holds each to the generated schema, so a changed +kubebuilder:default
// cannot leave a Go-created profile on the old value.
func TestPointerDefaultsMatchTheGeneratedCRD(t *testing.T) {
	raw, err := os.ReadFile("../../config/crd/bases/transcode.clustarr.io_transcodeprofiles.yaml")
	require.NoError(t, err)
	var crd map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	defaultAt := func(path ...string) any {
		t.Helper()
		node := crd["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any)["schema"].(map[string]any)["openAPIV3Schema"]
		for _, p := range append([]string{"spec"}, path...) {
			node = node.(map[string]any)["properties"].(map[string]any)[p]
			require.NotNilf(t, node, "spec.%v is not in the generated CRD", path)
		}
		return node.(map[string]any)["default"]
	}
	for _, path := range [][]string{
		{"audio", "dropCommentary"},
		{"subtitles", "copyText"},
		{"subtitles", "copyBitmap"},
		{"subtitles", "copyAttachments"},
		{"policy", "skipIfCompliant"},
		{"policy", "remuxOnlyWhenVideoCompliant"},
		{"policy", "replaceSource"},
		{"policy", "recycleBin"},
		{"verify", "packetCount"},
	} {
		assert.Equalf(t, true, defaultAt(path...), "ProfileSpec reads a nil spec.%v as true", path)
	}
	d, err := time.ParseDuration(defaultAt("policy", "minDuration").(string))
	require.NoError(t, err)
	assert.Equal(t, DefaultMinDuration, d)
	assert.EqualValues(t, DefaultMaxOutputToSourcePercent, defaultAt("policy", "maxOutputToSourcePercent"))
}

// G4-0 made every other defaulted-true bool in the spec a pointer, plus
// policy.minDuration and policy.maxOutputToSourcePercent, whose zero means
// something ("consider every file", "no size check") that a Go client could
// not otherwise send. Unset must convert to the CRD default and an explicit
// zero to zero, through the one converter every consumer uses.
func TestProfileSpecAppliesPointerDefaults(t *testing.T) {
	unset := ProfileSpec(transcodev1alpha1.TranscodeProfileSpec{}, nil)
	assert.True(t, unset.Audio.DropCommentary)
	assert.True(t, unset.Subtitles.CopyText)
	assert.True(t, unset.Subtitles.CopyBitmap)
	assert.True(t, unset.Subtitles.CopyAttachments)
	assert.True(t, unset.Policy.SkipIfCompliant)
	assert.True(t, unset.Policy.RemuxOnlyWhenVideoCompliant)
	assert.True(t, unset.Verify.PacketCount)
	assert.Equal(t, time.Minute, unset.Policy.MinDuration)
	assert.Equal(t, int32(100), unset.Policy.MaxOutputToSourcePercent)

	off := ProfileSpec(transcodev1alpha1.TranscodeProfileSpec{
		Audio:     transcodev1alpha1.AudioSpec{DropCommentary: ptr.To(false)},
		Subtitles: transcodev1alpha1.SubSpec{CopyText: ptr.To(false), CopyBitmap: ptr.To(false), CopyAttachments: ptr.To(false)},
		Policy: transcodev1alpha1.PolicySpec{
			SkipIfCompliant: ptr.To(false), RemuxOnlyWhenVideoCompliant: ptr.To(false),
			MinDuration: &metav1.Duration{}, MaxOutputToSourcePercent: ptr.To[int32](0),
		},
		Verify: transcodev1alpha1.VerifySpec{PacketCount: ptr.To(false)},
	}, nil)
	assert.False(t, off.Audio.DropCommentary)
	assert.False(t, off.Subtitles.CopyText)
	assert.False(t, off.Subtitles.CopyBitmap)
	assert.False(t, off.Subtitles.CopyAttachments)
	assert.False(t, off.Policy.SkipIfCompliant)
	assert.False(t, off.Policy.RemuxOnlyWhenVideoCompliant)
	assert.False(t, off.Verify.PacketCount)
	assert.Zero(t, off.Policy.MinDuration)
	assert.Zero(t, off.Policy.MaxOutputToSourcePercent)
}

// An auto profile with no class chosen yet plans for CPU. Its profile hash is
// therefore the one a cpu profile had, so changing the CRD default from cpu to
// auto re-transcodes nothing.
func TestProfileSpecResolvesAutoToCPU(t *testing.T) {
	auto := transcodev1alpha1.TranscodeProfileSpec{Hardware: transcodev1alpha1.HardwareAuto}
	cpu := transcodev1alpha1.TranscodeProfileSpec{Hardware: transcodev1alpha1.HardwareCPU}
	assert.Equal(t, ProfileSpec(cpu, nil), ProfileSpec(auto, nil))
	nv := transcodev1alpha1.HardwareNVIDIA
	assert.Equal(t, ProfileSpec(transcodev1alpha1.TranscodeProfileSpec{Hardware: nv}, nil), ProfileSpec(auto, &nv),
		"a chosen class overrides auto")
	autoOverride := transcodev1alpha1.HardwareAuto
	assert.Equal(t, ProfileSpec(cpu, nil), ProfileSpec(cpu, &autoOverride), "an auto override of a pinned profile keeps cpu")
}

func assertNoZeroLeaf(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	if v.Kind() == reflect.Struct {
		for i := range v.NumField() {
			assertNoZeroLeaf(t, v.Field(i), path+"."+v.Type().Field(i).Name)
		}
		return
	}
	assert.Falsef(t, v.IsZero(), "%s was not carried over", path)
}

// Progress arrives from ffmpeg once a second. The reporter must turn a
// burst of samples into at most one apply per interval, and must apply the
// LAST sample when stopped, so the final state is never lost to throttling.
func TestProgressReporterThrottlesAndFlushesTheLastSample(t *testing.T) {
	var mu sync.Mutex
	var applied []transcodev1alpha1.Progress
	apply := func(_ context.Context, p transcodev1alpha1.Progress) error {
		mu.Lock()
		defer mu.Unlock()
		applied = append(applied, p)
		return nil
	}
	rep := newProgressReporter(time.Hour, 10_000, time.Now, apply)
	ctx := context.Background()
	rep.start(ctx)
	for i := int64(1); i <= 50; i++ {
		rep.observe(transcode.Progress{Frame: i, OutTimeMillis: i * 100})
	}
	rep.stop(ctx)

	require.Len(t, applied, 1, "fifty samples inside one interval must be one apply")
	assert.Equal(t, int64(50), applied[0].Frame, "the apply on stop must carry the last sample")
	assert.Equal(t, int32(50), applied[0].Percent, "5000ms of 10000ms")
	assert.False(t, applied[0].UpdatedAt.IsZero())
}

func TestProgressReporterRetriesAFailedApplyAndAppliesNothingWhenIdle(t *testing.T) {
	calls := 0
	fail := true
	apply := func(context.Context, transcodev1alpha1.Progress) error {
		calls++
		if fail {
			return errors.New("apiserver blip")
		}
		return nil
	}
	rep := newProgressReporter(time.Hour, 0, time.Now, apply)
	ctx := context.Background()

	rep.flush(ctx)
	assert.Zero(t, calls, "no sample, no apply")

	rep.observe(transcode.Progress{Frame: 1})
	rep.flush(ctx)
	fail = false
	rep.flush(ctx)
	assert.Equal(t, 2, calls, "a failed apply is retried on the next tick")
	rep.flush(ctx)
	assert.Equal(t, 2, calls, "and not re-applied once it landed")
}

func TestPercentOf(t *testing.T) {
	assert.Equal(t, int32(0), percentOf(transcode.Progress{OutTimeMillis: 500}, 0), "no duration, no percentage")
	assert.Equal(t, int32(25), percentOf(transcode.Progress{OutTimeMillis: 250}, 1000))
	assert.Equal(t, int32(99), percentOf(transcode.Progress{OutTimeMillis: 1200}, 1000), "only progress=end says 100")
	assert.Equal(t, int32(100), percentOf(transcode.Progress{Percent: 100}, 1000))
	assert.Equal(t, int32(0), percentOf(transcode.Progress{OutTimeMillis: -5}, 1000))
}

// OutputPath is gap-fix ruling R-11's output location, shared by the
// controller's plan and the worker's write.
func TestOutputPath(t *testing.T) {
	const src = "/data/media/movies/Film (2020)/Film (2020).mkv"
	spec := func(source string, out *string) transcodev1alpha1.TranscodeJobSpec {
		return transcodev1alpha1.TranscodeJobSpec{SourcePath: source, OutputPath: out}
	}
	for _, tc := range []struct {
		name      string
		spec      transcodev1alpha1.TranscodeJobSpec
		container transcodev1alpha1.Container
		replace   bool
		want      string
		wantErr   string
	}{
		{name: "same container replaces in place", spec: spec(src, nil), container: "mkv", replace: true, want: src},
		{name: "an empty container is mkv", spec: spec(src, nil), replace: true, want: src},
		{
			name: "case does not make a change", spec: spec("/data/media/movies/F/F.MKV", nil), container: "mkv", replace: true,
			want: "/data/media/movies/F/F.MKV",
		},
		{
			name: "container change gets the new extension", spec: spec("/data/media/movies/F/F.mp4", nil), container: "mkv", replace: true,
			want: "/data/media/movies/F/F.mkv",
		},
		{
			name: "mkv to mp4", spec: spec(src, nil), container: "mp4", replace: true,
			want: "/data/media/movies/Film (2020)/Film (2020).mp4",
		},
		{
			name: "a kept source takes a version name", spec: spec(src, nil), container: "mkv", replace: false,
			want: "/data/media/movies/Film (2020)/Film (2020) - hevc.mkv",
		},
		{
			name: "a kept source across a container change", spec: spec("/data/media/movies/F/F.avi", nil), container: "mkv", replace: false,
			want: "/data/media/movies/F/F - hevc.mkv",
		},
		{
			name: "spec.outputPath wins", spec: spec(src, ptr.To("/data/media/movies/Other/Film.mkv")), container: "mkv", replace: true,
			want: "/data/media/movies/Other/Film.mkv",
		},
		{
			name: "spec.outputPath is cleaned", spec: spec(src, ptr.To("/data/media/movies/./Other//Film.mkv")), container: "mkv", replace: false,
			want: "/data/media/movies/Other/Film.mkv",
		},
		{
			name: "spec.outputPath must match the container", spec: spec(src, ptr.To("/data/media/x.mp4")), container: "mkv", replace: true,
			wantErr: "extension",
		},
		{
			name: "spec.outputPath must be absolute", spec: spec(src, ptr.To("x.mkv")), container: "mkv", replace: true,
			wantErr: "not absolute",
		},
		{
			name: "a kept source cannot be the output", spec: spec(src, ptr.To(src)), container: "mkv", replace: false,
			wantErr: "keeps the source",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OutputPath(tc.spec, "hevc", tc.container, tc.replace)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// The controller stamps its span's traceparent on each Job, and the worker
// continues that trace from it; nothing, or garbage, leaves the context as
// it was.
func TestTraceParentRoundTrips(t *testing.T) {
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	tp := TraceParent(trace.ContextWithSpanContext(context.Background(), sc))
	require.Equal(t, "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01", tp)

	got := trace.SpanContextFromContext(ContextWithTraceParent(context.Background(), tp))
	assert.Equal(t, sc.TraceID(), got.TraceID())
	assert.Equal(t, sc.SpanID(), got.SpanID())
	assert.True(t, got.IsRemote())

	assert.Empty(t, TraceParent(context.Background()), "no span, no traceparent")
	for _, bad := range []string{"", "not-a-traceparent"} {
		assert.False(t, trace.SpanContextFromContext(ContextWithTraceParent(context.Background(), bad)).IsValid())
	}
}
