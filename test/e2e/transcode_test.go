//go:build e2e

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

// Task E-5 -- transcode fixtures and Phase H scenario 12 (M4, squasharr).
//
// Phase E's code is complete and wired (E-4, 35c298e/b001001): squasharr
// registers the TranscodeProfile and TranscodeJob controllers and the slot
// scheduler; config/manager/squasharr.yaml (composed into config/e2e
// through config/default) ships the squasharr ServiceAccount and a
// --slots cpu=2,nvidia=1,intel=1 budget. --worker-image/--worker-image-cuda
// (CLUSTARR_WORKER_IMAGE(_CUDA)) point at the dedicated transcoder/
// transcoder-cuda images (build: Dockerfile.transcoder), not the media
// image, since the encoding runtime moved out of media; hack/e2e.sh's
// `make docker-build` builds and kind-loads it and waits on
// deployment/squasharr's rollout. Nothing in config/e2e needed adding for
// this file's scenario to reach a real worker pod -- verified by reading
// config/manager/squasharr.yaml, config/rbac/kustomization.yaml,
// config/default/kustomization.yaml and hack/e2e.sh's own WORKLOADS list,
// not assumed.
//
// X14 retired the per-Job `squasharr-worker` ServiceAccount, ClusterRole
// and binding (config/rbac/squasharr_worker_role*.yaml, and `clustarr
// squasharr --role worker`) along with them: the transcode itself now runs
// on per-(profile, class) pool Jobs whose pods carry no ServiceAccount at
// all and report over NATS, not the apiserver (design spec §18.1, §18.2).
// A pool Job's pod is what this scenario now waits on reaching Running.
//
// This file adds no static TranscodeProfile manifest to config/e2e either,
// deliberately: newTranscodeProfile below scopes every profile it creates to
// an exact (resolution, source) label pair no other scenario's real
// MediaFile ever carries, so it can never pick up another scenario's file.
// A shared, cluster-wide profile (the config/e2e/quality-profile.yaml
// pattern) would have the opposite property -- spec.default=true selects
// EVERY video MediaFile in the shared cluster for as long as the profile
// lives, which is unlike QualityProfile (read, never mutates anything) and
// would create TranscodeJobs (and eventually spend real transcode Job slots)
// against every other scenario's planted media for the life of a shared
// `make e2e` run.
//
// # What this suite has never been executed against
//
// Like every file in this package (main_test.go's own doc comment), this
// has never run against a kind cluster. Every wait below is written to fail,
// or skip, with a NAMED reason rather than hang for its full timeout when a
// dependency turns out to be missing.
//
// # Why scenario 12 does not reuse the HDR10 fixture
//
// This task also adds an HDR10 clip to images/Dockerfile.e2e-fixtures
// (HEVC 10-bit, BT.2020 primaries, SMPTE ST 2084 transfer, mastering-display
// and content-light metadata) and test/fixtures/seed copies it out
// alongside the plain probe clip. Its ENTIRE proof obligation, per this
// task's brief, is the UNIT test pkg/mediainfo/hdr10_fixture_test.go -- a
// clip ffmpeg writes but pkg/mediainfo.ClassifyHDR reads as SDR proves
// nothing, so that test, not an e2e assertion, is what actually establishes
// the recipe works.
//
// Deliberately NOT reused here: the source this scenario needs is one a
// TranscodeProfile's Go-zero-value (CRD-defaulted) policy decides to
// actually ENCODE, so the real worker Job path gets exercised. The
// suite's ordinary probe clip (test/fixtures/seed.ClipName) is h264/AAC,
// which the profile's hevc/yuv420p10le default never finds compliant.
// The HDR10 clip, by contrast, IS already HEVC 10-bit -- exactly what
// squasharr transcodes files TO (CLAUDE.md: "Transcode to HEVC 10-bit +
// AAC") -- so a profile with the same codec/pixel-format defaults would plan
// a remux or a skip against it (policy.skipIfCompliant,
// policy.remuxOnlyWhenVideoCompliant, both CRD-default true), not the real
// encode this scenario exists to prove end to end. Using it here would also
// need an audio track (the HDR10 clip is video-only, a single lavfi source,
// and pkg/transcode's planner works from real streams), further diverging
// this scenario from the thing it is actually testing.
//
// # Dolby Vision
//
// Cannot be synthesized with ffmpeg alone -- see
// TestTranscodeDolbyVisionSkipped's own doc comment.
//
// # Extending download scenario 1
//
// TestDownloadScenario1TranscodeLeg (this file) drives scenario 1's
// download-import route to a real MediaFile and through a real
// TranscodeJob. It used to be a permanent skip -- the download-import
// route could never reach a MediaFile at all, because test/fixtures/seeder
// and test/fixtures/nntpstub named their content "clustarr-fixture.bin",
// outside pkg/fsops.MediaExtensions -- until X12c (docs/superpowers/plans/
// 2026-09-23-gap-fixes.md) fixed both fixtures (test/fixtures/**, out of
// E-5's own file scope but squarely X12c's).
package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	transcodejobctrl "github.com/mediactl/clustarr/app/squash/controller/transcodejob"
	"github.com/mediactl/clustarr/pkg/pipeline"
)

const (
	// transcodeMovieFolder/File plant the file scenario 12's main flow
	// transcodes. The folder embeds the provider id in the form
	// pkg/release/ids.go recognises ("{tmdb-N}"), reusing fixtureTmdbID
	// (libraryscan_test.go) since it is the only id the in-cluster TMDB stub
	// can answer for -- though this scenario never waits on that metadata
	// (see the main test's own comment), only on the MediaFile.
	//
	// "2160p" + "WEB-DL" is chosen so pkg/release freezes
	// spec.quality.{resolution,source} = {2160, webdl}: a signature no
	// other scenario in this package ever plants a REAL (plantMedia + full
	// scan) MediaFile with. Every other real MediaFile in test/e2e is
	// 1080p/bluray (fixtureMovieFolder/File, libraryscan_test.go) --
	// confirmed by grepping this package for "1080p\|720p\|2160p\|BluRay\|
	// WEB-DL" before choosing these tokens, not assumed. series_test.go's
	// 720p files use plantFiller, never probed into a real MediaFile, so
	// they cannot collide with transcodeContainerFolder/File's 720p/webdl
	// signature below either.
	transcodeMovieFolder = "Fixture Transcode Movie (2019) {tmdb-27205}"
	transcodeMovieFile   = "Fixture.Transcode.Movie.2019.2160p.WEB-DL.x264-CLUSTARR.mkv"

	// transcodeContainerFolder/File plant TestTranscodeContainerChangeMovesTheFile's
	// file: a distinct (720, webdl) signature, same reasoning as above.
	transcodeContainerFolder = "Fixture Transcode Container Movie (2019) {tmdb-27205}"
	transcodeContainerFile   = "Fixture.Transcode.Container.Movie.2019.720p.WEB-DL.x264-CLUSTARR.mkv"

	// defaultRecycleBinLogical mirrors RootFolderSpec.RecycleBin.Path's own
	// CRD default (api/catalog/v1alpha1/rootfolder_types.go) and
	// app/squash/worker/paths.go's identical code-level floor
	// (defaultRecycleBin) for when a RootFolder's is somehow empty. Neither
	// is imported here: the CRD default is a struct tag on a type in
	// another package's API, and paths.go's constant is unexported.
	// newRootFolder (helpers_test.go) never sets RecycleBin.Path, so every
	// RootFolder this suite creates -- this scenario's included -- recycles
	// into exactly this path.
	defaultRecycleBinLogical = "/data/.recycle"

	// transcodeProfileReadyTimeout covers one TranscodeProfile reconcile: a
	// pure computation (hash, selector match, count) with no external
	// call -- the same order of magnitude as newRankedQualityProfile's own
	// 2-minute wait for QualityProfileConditionReady.
	transcodeProfileReadyTimeout = 2 * time.Minute

	// transcodeJobCreatedTimeout covers the round trip from a probed
	// MediaFile through transcodeprofile's mapper (watch-driven, no
	// redelivery ladder -- app/squash/controller/transcodeprofile watches
	// MediaFile on status.probeHash changing and TranscodeProfile itself)
	// to a created TranscodeJob.
	transcodeJobCreatedTimeout = 2 * time.Minute

	// transcodeJobPlannedTimeout covers one TranscodeJob reconcile's plan()
	// step. Ruling R3: the controller role never mounts /data, so planning
	// is pure computation from the MediaFile's stored probe and the
	// profile -- no real work happens before Planned.
	transcodeJobPlannedTimeout = 2 * time.Minute

	// transcodeJobSucceededTimeout covers what plannedTimeout does not: a
	// REAL batch Job -- pod scheduling and image pull (generous margin,
	// mirroring engineReadyTimeout's own reasoning for grabarr's engine
	// StatefulSet, since the worker image may not already be warm on the
	// node), admission against the slot budget (config/manager/
	// squasharr.yaml ships --slots cpu=2,..., so this scenario's one job is
	// admitted on the very first admission pass, never queued behind
	// another scenario -- squasharr is the only component that creates
	// TranscodeJobs, and this file's two scenarios are the only ones that
	// create TranscodeProfiles), then squasharr's worker actually running
	// ffmpeg (x265, the profile's CRD-default "slow" preset) against the
	// suite's ten-second 640x480 probe clip, verifying the output and
	// swapping it over the source (R5: hard-link the original into the
	// recycle bin, then rename the output over the source path). A
	// ten-second 640x480 clip encodes in well under a minute on any real
	// node at any x265 preset; this is sized for a loaded single kind node
	// and a cold image pull, not the encode itself.
	transcodeJobSucceededTimeout = 10 * time.Minute

	// recycledOriginalTimeout bounds the wait for the pre-transcode
	// original to appear in the recycle bin. It is not additive with
	// transcodeJobSucceededTimeout's own wait: the worker's swap happens
	// strictly before the Job exits 0 (R5's ordering), so by the time
	// TranscodeJob reports Succeeded the recycled file already exists on
	// disk; this only covers the hostPath mount and this process's own
	// os.Stat catching up.
	recycledOriginalTimeout = 2 * time.Minute

	// transcodeSwapIncorporatedTimeout covers catalogarr's MediaFile
	// controller noticing the newest Succeeded TranscodeJob
	// (mediafile_controller.go's latestUnincorporatedTranscode, a
	// watch-driven reconcile fired by the Succeeded phase transition) and
	// re-probing -- one real ffprobe run against the swapped-in
	// (transcoded, much smaller) output.
	transcodeSwapIncorporatedTimeout = 3 * time.Minute

	// poolResumedTimeout bounds the wait for a (profile, class) pool Job to
	// come off suspend once admission dispatches a task to it -- pod
	// scheduling and an image pull, the same order of magnitude as
	// transcodeJobSucceededTimeout's own margin for the identical reason,
	// but the pool need only start, not finish an encode.
	poolResumedTimeout = 2 * time.Minute

	// poolSuspendedTimeout bounds the wait for a pool Job to go back to
	// suspend once its only task has finished and the Job controller has
	// let its pods go (app/squash/controller/pool.Mutable's own gate).
	poolSuspendedTimeout = 2 * time.Minute
)

// newTranscodeProfile creates a cluster-scoped TranscodeProfile that selects
// MediaFiles by an EXACT (resolution, source) label pair --
// catalog.clustarr.io/resolution and catalog.clustarr.io/source, which
// app/catalog/controller/mediafile/labels.go's mirrorLabels sets from the
// frozen-at-import Quality every real MediaFile in this suite carries -- and
// waits for it to reach Ready with a non-empty status.hash (the controller's
// own gate before it will plan or tag anything against this profile:
// app/squash/controller/transcodejob/controller.go's plan() waits on exactly
// this field too).
//
// container overrides the CRD's own "mkv" default; every other field is
// left at its Go zero value, relying on the real apiserver's
// structural-schema defaulting for every leaf field that has one
// (Video.Codec="hevc" and siblings, Policy.*, Verify.*) -- the same reliance
// newRootFolder/newIndexer/newMovie already place on CRD defaults
// throughout this suite for a typed client Create against a REAL apiserver
// (kind's, here; envtest's elsewhere), as opposed to a fake client, which
// applies no defaulting at all. The three profile fields whose Go zero
// value never marshals as genuinely absent (ActiveDeadline, Resources,
// Scratch -- metav1.Duration and resource.Quantity both always emit a
// quoted value, never omit) are floored in Go by squasharr's own buildJob
// (app/squash/controller/transcodejob/job.go's activeDeadlineFor/
// resourcesFor/scratchFor), not by CRD defaulting, so they need no value
// here either.
func newTranscodeProfile(
	ctx context.Context, t *testing.T, prefix string,
	resolution int32, source commonv1.Source, container transcodev1alpha1.Container,
) *transcodev1alpha1.TranscodeProfile {
	t.Helper()
	tp := &transcodev1alpha1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName(prefix)},
		Spec: transcodev1alpha1.TranscodeProfileSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
				catalogv1alpha1.LabelKind:       string(commonv1.MediaKindMovie),
				catalogv1alpha1.LabelResolution: strconv.Itoa(int(resolution)),
				catalogv1alpha1.LabelSource:     string(source),
			}},
			Container: container,
			Hardware:  transcodev1alpha1.HardwareCPU,
		},
	}
	require.NoError(t, k8sClient.Create(ctx, tp))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), tp) })

	key := client.ObjectKey{Name: tp.Name}
	waitFor(t, ctx, transcodeProfileReadyTimeout, "TranscodeProfile "+tp.Name+" Ready",
		func(ctx context.Context) (bool, error) {
			var live transcodev1alpha1.TranscodeProfile
			if err := k8sClient.Get(ctx, key, &live); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			if live.Status.Hash == "" {
				return false, nil
			}
			*tp = live
			return isConditionTrue(live.Status.Conditions, transcodev1alpha1.TranscodeProfileConditionReady), nil
		}, describeTranscodeProfile(key))
	return tp
}

// describeTranscodeProfile renders one TranscodeProfile's status for a
// failure message.
func describeTranscodeProfile(key client.ObjectKey) func() string {
	return func() string {
		var live transcodev1alpha1.TranscodeProfile
		if err := k8sClient.Get(context.Background(), key, &live); err != nil {
			return fmt.Sprintf("TranscodeProfile %s could not be read back: %v", key.Name, err)
		}
		out := fmt.Sprintf("TranscodeProfile %s hash=%q matchingFiles=%d pendingJobs=%d runningJobs=%d",
			key.Name, live.Status.Hash, live.Status.MatchingFiles, live.Status.PendingJobs, live.Status.RunningJobs)
		for _, c := range live.Status.Conditions {
			out += fmt.Sprintf("\n    condition %s=%s reason=%s message=%q", c.Type, c.Status, c.Reason, c.Message)
		}
		return out
	}
}

// describeTranscodeJob renders one TranscodeJob's status for a failure
// message.
func describeTranscodeJob(key client.ObjectKey) func() string {
	return func() string {
		var live transcodev1alpha1.TranscodeJob
		if err := k8sClient.Get(context.Background(), key, &live); err != nil {
			return fmt.Sprintf("TranscodeJob %s could not be read back: %v", key.Name, err)
		}
		mode := "<no plan>"
		if live.Status.Plan != nil {
			mode = string(live.Status.Plan.Mode)
		}
		jobRef := "<none>"
		if live.Status.JobRef != nil {
			jobRef = *live.Status.JobRef
		}
		out := fmt.Sprintf("TranscodeJob %s phase=%q plan.mode=%s jobRef=%s attempts=%d message=%q",
			key.Name, live.Status.Phase, mode, jobRef, live.Status.Attempts, live.Status.Message)
		if live.Status.Progress != nil {
			out += fmt.Sprintf("\n    progress percent=%d frame=%d", live.Status.Progress.Percent, live.Status.Progress.Frame)
		}
		if live.Status.Result != nil {
			out += fmt.Sprintf("\n    result outputPath=%q outputSizeBytes=%d outputToSourcePercent=%d",
				live.Status.Result.OutputPath, live.Status.Result.OutputSizeBytes, live.Status.Result.OutputToSourcePercent)
		}
		if live.Status.StderrTail != "" {
			out += fmt.Sprintf("\n    stderrTail=%s", live.Status.StderrTail)
		}
		for _, c := range live.Status.Conditions {
			out += fmt.Sprintf("\n    condition %s=%s reason=%s message=%q", c.Type, c.Status, c.Reason, c.Message)
		}
		return out
	}
}

// transcodeJobsForMediaFile lists every TranscodeJob in namespace whose
// spec.mediaFileRef is mediaFileName.
func transcodeJobsForMediaFile(ctx context.Context, t *testing.T, namespace, mediaFileName string) []transcodev1alpha1.TranscodeJob {
	t.Helper()
	var list transcodev1alpha1.TranscodeJobList
	require.NoError(t, k8sClient.List(ctx, &list, client.InNamespace(namespace)))
	var out []transcodev1alpha1.TranscodeJob
	for _, tj := range list.Items {
		if tj.Spec.MediaFileRef == mediaFileName {
			out = append(out, tj)
		}
	}
	return out
}

// waitForTranscodeJobForMediaFile waits for transcodeprofile's mapper
// (app/squash/controller/transcodeprofile/controller.go's ensureTranscodeJob)
// to create exactly one TranscodeJob for mediaFileName and returns it.
// transcodeJobName is deterministic on (MediaFile, profile hash) precisely
// so this is "exactly one", not "at least one".
func waitForTranscodeJobForMediaFile(ctx context.Context, t *testing.T, namespace, mediaFileName string) transcodev1alpha1.TranscodeJob {
	t.Helper()
	var got transcodev1alpha1.TranscodeJob
	waitFor(t, ctx, transcodeJobCreatedTimeout, "TranscodeJob for MediaFile "+mediaFileName,
		func(ctx context.Context) (bool, error) {
			jobs := transcodeJobsForMediaFile(ctx, t, namespace, mediaFileName)
			if len(jobs) == 0 {
				return false, nil
			}
			require.Lenf(t, jobs, 1, "transcodeprofile's mapper must name exactly one TranscodeJob "+
				"per (MediaFile, profile hash) pair, deterministically: %+v", jobs)
			got = jobs[0]
			return true, nil
		})
	return got
}

// waitForTranscodeJobPhase waits for tj to reach EXACTLY want, failing fast
// (rather than waiting out the full timeout) on an unexpected Failed unless
// want IS Failed. Only for a phase that cannot be skipped past in the
// apiserver's persisted status -- a terminal one (Succeeded, Failed,
// Skipped), which advance() always returns from before its next status
// apply. For an intermediate phase like Planned, use
// waitForTranscodeJobPhaseAtLeast instead: app/squash/controller/transcodejob's
// advance() runs plan() and ensureJob() in the SAME Reconcile call when
// nothing blocks either, so a fresh job's FIRST persisted status can already
// read Queued (or later) -- Planned was true only for an instant inside that
// one Go call and was never independently observable over the API.
func waitForTranscodeJobPhase(ctx context.Context, t *testing.T, key client.ObjectKey, timeout time.Duration, want transcodev1alpha1.TranscodeJobPhase) transcodev1alpha1.TranscodeJob {
	t.Helper()
	var live transcodev1alpha1.TranscodeJob
	waitFor(t, ctx, timeout, "TranscodeJob "+key.Name+" reaching phase "+string(want),
		func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, key, &live); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			if live.Status.Phase == transcodev1alpha1.TranscodeJobPhaseFailed && want != transcodev1alpha1.TranscodeJobPhaseFailed {
				return false, fmt.Errorf("TranscodeJob %s reached Failed (message=%q)", key.Name, live.Status.Message)
			}
			return live.Status.Phase == want, nil
		}, describeTranscodeJob(key))
	return live
}

// transcodeJobPhaseOrder is the normal, non-terminal-surprise lifecycle a
// TranscodeJob progresses through on its way to Succeeded, mirroring
// downloadPhaseOrder's (helpers_test.go) exact shape and the same reason for
// it: Skipped sits outside it on purpose, a decision not to progress rather
// than a step in progressing.
var transcodeJobPhaseOrder = []transcodev1alpha1.TranscodeJobPhase{
	transcodev1alpha1.TranscodeJobPhasePending,
	transcodev1alpha1.TranscodeJobPhasePlanned,
	transcodev1alpha1.TranscodeJobPhaseQueued,
	transcodev1alpha1.TranscodeJobPhaseRunning,
	transcodev1alpha1.TranscodeJobPhaseVerifying,
	transcodev1alpha1.TranscodeJobPhaseSucceeded,
}

func transcodeJobPhaseIndex(p transcodev1alpha1.TranscodeJobPhase) int {
	for i, v := range transcodeJobPhaseOrder {
		if v == p {
			return i
		}
	}
	return -1
}

// waitForTranscodeJobPhaseAtLeast waits until tj.status.phase is threshold
// or later in transcodeJobPhaseOrder -- see waitForTranscodeJobPhase's doc
// comment for why an intermediate phase needs this rather than an exact
// match. Failed and Skipped are always reported as errors (carrying
// status.message), since neither is "not yet" relative to any entry in the
// ordered list.
func waitForTranscodeJobPhaseAtLeast(ctx context.Context, t *testing.T, key client.ObjectKey, timeout time.Duration, threshold transcodev1alpha1.TranscodeJobPhase) transcodev1alpha1.TranscodeJob {
	t.Helper()
	want := transcodeJobPhaseIndex(threshold)
	require.GreaterOrEqualf(t, want, 0, "waitForTranscodeJobPhaseAtLeast: %q is not in transcodeJobPhaseOrder", threshold)
	var live transcodev1alpha1.TranscodeJob
	waitFor(t, ctx, timeout, fmt.Sprintf("TranscodeJob %s reaching phase %s or later", key.Name, threshold),
		func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, key, &live); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			switch live.Status.Phase {
			case transcodev1alpha1.TranscodeJobPhaseFailed:
				return false, fmt.Errorf("TranscodeJob %s reached Failed (message=%q)", key.Name, live.Status.Message)
			case transcodev1alpha1.TranscodeJobPhaseSkipped:
				return false, fmt.Errorf("TranscodeJob %s reached Skipped unexpectedly (message=%q)", key.Name, live.Status.Message)
			}
			return transcodeJobPhaseIndex(live.Status.Phase) >= want, nil
		}, describeTranscodeJob(key))
	return live
}

// waitForTranscodeJobField polls tj's live status through get until it
// returns a non-nil value, then returns that value. It is the generic
// sibling of waitForTranscodeJobPhase for a status field a phase threshold
// does not directly gate -- status.jobRef here, which dispatch.go sets in
// the same write that takes a job to Queued, but a poll can still observe
// between the two reads.
func waitForTranscodeJobField[T any](ctx context.Context, t *testing.T, key client.ObjectKey, timeout time.Duration, name string, get func(transcodev1alpha1.TranscodeJobStatus) *T) *T {
	t.Helper()
	var got *T
	waitFor(t, ctx, timeout, fmt.Sprintf("TranscodeJob %s field %s", key.Name, name),
		func(ctx context.Context) (bool, error) {
			var live transcodev1alpha1.TranscodeJob
			if err := k8sClient.Get(ctx, key, &live); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			if live.Status.Phase == transcodev1alpha1.TranscodeJobPhaseFailed {
				return false, fmt.Errorf("TranscodeJob %s reached Failed waiting for field %s (message=%q)", key.Name, name, live.Status.Message)
			}
			got = get(live.Status)
			return got != nil, nil
		}, describeTranscodeJob(key))
	return got
}

// waitForMediaFileSwapIncorporated waits until catalogarr's MediaFile
// controller has incorporated a transcode swap: spec.original explicitly
// false (mediafile_controller.go:255-258's WithOriginal(false), the one
// field that only ever flips once a swap lands) AND status.probedAt has
// advanced past sinceProbedAt, so a stale read racing the re-probe cannot
// pass.
func waitForMediaFileSwapIncorporated(ctx context.Context, t *testing.T, key client.ObjectKey, sinceProbedAt time.Time) catalogv1alpha1.MediaFile {
	t.Helper()
	var live catalogv1alpha1.MediaFile
	waitFor(t, ctx, transcodeSwapIncorporatedTimeout, "MediaFile "+key.Name+" transcode swap incorporated",
		func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, key, &live); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			return live.Spec.Original != nil && !*live.Spec.Original &&
				live.Status.ProbedAt != nil && live.Status.ProbedAt.After(sinceProbedAt), nil
		}, describeMediaFile(key))
	return live
}

// findCondition returns the condition of type condType, or nil.
func findCondition(conds []metav1.Condition, condType string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == condType {
			return &conds[i]
		}
	}
	return nil
}

// pipelineRowStageRe matches one pipeline row's data-kind, data-stage and
// data-ref attributes, in the fixed order ui/views/pipeline.templ's
// pipelineRow renders them, capturing data-stage's value. \s+ between
// attributes tolerates however templ actually whitespaces the rendered
// tag (a single space or a newline-indented one) -- the same tolerance
// ui_test.go's sseDownloadsPhase regex already needs for the SSE-framed
// version of this same markup.
func pipelineRowStageRe(kind commonv1.MediaKind, ref types.NamespacedName) *regexp.Regexp {
	return regexp.MustCompile(`data-kind="` + regexp.QuoteMeta(string(kind)) +
		`"\s+data-stage="([^"]*)"\s+data-ref="` + regexp.QuoteMeta(ref.String()) + `"`)
}

// pipelineRowStage extracts the data-stage value of the ONE pipeline row
// identified by kind and ref from a GET /pipeline body.
//
// ui_test.go's own doc comment (written before commit 067594f) says
// pipeline.templ carries no per-row identifying data attribute the way
// downloads.templ's data-download does, "only data-kind and data-stage" --
// true when it was written, no longer true: pipelineRow now also renders
// data-ref={ e.Ref.String() } (a types.NamespacedName, "<namespace>/<name>"),
// which is exactly the per-row key that file's own doc comment says the page
// lacks. This scenario uses it instead of that file's whole-body
// strings.Contains workaround, so an assertion about ONE row cannot be
// satisfied by a different row of the same kind elsewhere on a page this
// suite shares with every other scenario's Movies.
func pipelineRowStage(body string, kind commonv1.MediaKind, ref types.NamespacedName) (stage string, ok bool) {
	m := pipelineRowStageRe(kind, ref).FindStringSubmatch(body)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// TestTranscodeMediaFileThroughTranscodeJob is Phase H scenario 12
// (docs/superpowers/plans/2026-09-18-remaining-work.md): a MediaFile under a
// matching TranscodeProfile is transcoded end to end. A LibraryScan
// produces a real, probed MediaFile; a scoped TranscodeProfile
// (newTranscodeProfile) picks it up; squasharr's TranscodeProfile mapper
// creates a TranscodeJob; squasharr's TranscodeJob controller plans a real
// encode (h264 -> the profile's CRD-default hevc/yuv420p10le) and dispatches
// it to its (profile, class) pool -- a shared, long-lived batch Job that
// scales up from suspended zero to take the task and back down to suspended
// once it is the pool's only work (§7; there is no per-task Job any more,
// X14). squasharr's worker runs inside the pool's pod, transcodes, verifies,
// and swaps (ruling R5: hard-link the original into the RootFolder's
// recycle bin, then rename the output over the source path); catalogarr's
// MediaFile controller notices the Succeeded TranscodeJob and incorporates
// the swap, taking over spec.sizeBytes/modTime/original (§8.5); and the
// pipeline page reflects the transcode stage via ui/views/pipeline.templ's
// data-stage/data-ref attributes (D3 ruling R8: identify a row by a stable
// attribute, never by prose that could change independently of the markup).
//
// See this file's package doc comment for why the source is the suite's
// ordinary h264/AAC probe clip, not the HDR10 fixture this task also adds.
func TestTranscodeMediaFileThroughTranscodeJob(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()

	rf := newRootFolder(ctx, t, "e2e-tj-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	filePath := path.Join(rf.Spec.Path, transcodeMovieFolder, transcodeMovieFile)
	plantMedia(t, hostPath(filePath))
	runScan(ctx, t, rf, catalogv1alpha1.ScanModeFull)

	files := waitForMediaFileCount(ctx, t, rf.Spec.Path, 1)
	mfKey := client.ObjectKeyFromObject(&files[0])
	mf := waitForMediaFileProbed(ctx, t, mfKey)
	require.Equal(t, int32(2160), mf.Spec.Quality.Resolution,
		"pkg/release must have parsed the 2160p token from the planted filename")
	require.Equal(t, commonv1.SourceWebDL, mf.Spec.Quality.Source,
		"pkg/release must have parsed the WEB-DL token from the planted filename")
	originalSizeBytes := mf.Spec.SizeBytes
	require.EqualValues(t, len(sampleClip(t)), originalSizeBytes, "the planted file's frozen spec.sizeBytes must be the seeded clip's real size")
	firstProbedAt := mf.Status.ProbedAt
	require.NotNil(t, firstProbedAt)

	movieName := mf.Spec.MediaRef.Name
	cleanupUnlessFailed(t, func() {
		_ = k8sClient.Delete(context.Background(), &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: movieName, Namespace: Namespace},
		})
	})

	newTranscodeProfile(ctx, t, "e2e-tj-profile", 2160, commonv1.SourceWebDL, transcodev1alpha1.ContainerMKV)

	tj := waitForTranscodeJobForMediaFile(ctx, t, Namespace, mf.Name)
	tjKey := client.ObjectKeyFromObject(&tj)
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), &tj) })

	// Planned or later, not exactly Planned: see
	// waitForTranscodeJobPhase's doc comment -- plan() and ensureJob() run
	// in the same Reconcile call, so a fast reconcile's first PERSISTED
	// status can already read Queued.
	planned := waitForTranscodeJobPhaseAtLeast(ctx, t, tjKey, transcodeJobPlannedTimeout, transcodev1alpha1.TranscodeJobPhasePlanned)
	require.NotNil(t, planned.Status.Plan)
	require.Equal(t, transcodev1alpha1.PlanModeTranscode, planned.Status.Plan.Mode,
		"an h264 source under the profile's hevc default must plan a real encode, not a skip or remux")

	// §7: the job's task went to its profile's pool, which scaled up from
	// zero. status.jobRef names the pool Job -- squasharr dispatches to a
	// long-lived, shared (profile, class) pool now, never a per-task Job
	// (X14; app/squash/controller/pool.Name).
	poolName := *waitForTranscodeJobField(ctx, t, tjKey, transcodeJobPlannedTimeout, "jobRef",
		func(s transcodev1alpha1.TranscodeJobStatus) *string { return s.JobRef })
	poolKey := types.NamespacedName{Namespace: Namespace, Name: poolName}
	var poolJob batchv1.Job
	waitFor(t, ctx, poolResumedTimeout, "pool "+poolName+" resumed from zero",
		func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, poolKey, &poolJob); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			return !ptr.Deref(poolJob.Spec.Suspend, true), nil
		})
	assert.False(t, ptr.Deref(poolJob.Spec.Template.Spec.AutomountServiceAccountToken, true),
		"a pool pod carries no ServiceAccount token (X14)")

	running := waitForTranscodeJobPhaseAtLeast(ctx, t, tjKey, transcodeJobSucceededTimeout, transcodev1alpha1.TranscodeJobPhaseRunning)
	assert.NotEmpty(t, running.Status.WorkerPod, "a running job names its worker pod")

	succeeded := waitForTranscodeJobPhase(ctx, t, tjKey, transcodeJobSucceededTimeout, transcodev1alpha1.TranscodeJobPhaseSucceeded)
	require.NotNil(t, succeeded.Status.Result)
	require.NotEmpty(t, succeeded.Status.Result.OutputPath)
	require.NotZero(t, succeeded.Status.Result.OutputSizeBytes)

	// The pool drains and suspends back to zero once its only task has
	// finished (app/squash/controller/pool.Mutable's own gate; next.go).
	waitFor(t, ctx, poolSuspendedTimeout, "pool "+poolName+" suspended after its only job finished",
		func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, poolKey, &poolJob); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			return ptr.Deref(poolJob.Spec.Suspend, false), nil
		})

	// The original lands in the RootFolder's recycle bin (ruling R5 as
	// superseded in mechanism by E-3, 797d9f3: RecycleLink hard-links the
	// original into root/<today's UTC date>/<basename> BEFORE the output is
	// renamed over the source path, so a crash mid-swap never strands a
	// hole in the library). Its size must equal the ORIGINAL clip's size,
	// not the (typically much smaller, re-encoded) output's -- proving this
	// really is the pre-transcode file, not a copy of the swapped-in result.
	recycled := hostPath(path.Join(defaultRecycleBinLogical, time.Now().UTC().Format("2006-01-02"), transcodeMovieFile))
	waitFor(t, ctx, recycledOriginalTimeout, "recycled original at "+recycled, func(context.Context) (bool, error) {
		info, err := os.Stat(recycled)
		if err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return info.Size() == originalSizeBytes, nil
	})

	// catalogarr re-probes the swapped-in output at the SAME path and takes
	// over spec.sizeBytes/modTime/original.
	swapped := waitForMediaFileSwapIncorporated(ctx, t, mfKey, firstProbedAt.Time)
	require.NotEqual(t, originalSizeBytes, swapped.Spec.SizeBytes,
		"spec.sizeBytes must be the transcoded output's size, not the original clip's")
	require.NotNil(t, swapped.Status.Transcode, "status.transcode must be set once the swap is incorporated")
	require.Equal(t, catalogv1alpha1.TranscodeResultSucceeded, swapped.Status.Transcode.LastResult)
	require.NotEmpty(t, swapped.Status.Transcode.ProfileTag)
	require.NotNil(t, swapped.Status.MediaInfo)
	require.Equal(t, "hevc", swapped.Status.MediaInfo.VideoCodec, "the re-probe must read the transcoded output's real codec")
	require.EqualValues(t, 10, swapped.Status.MediaInfo.VideoBitDepth, "the profile's default pixelFormat is yuv420p10le")

	// The pipeline page reflects the transcode stage. pkg/pipeline.Project
	// (project.go) reaches StageComplete, not StageTranscodeDone, once
	// subtitles are ALSO satisfied -- but nothing in this scenario ever
	// creates a satisfied SubtitleRequest (captionarr is Phase F's surface,
	// out of scope here), so Complete is not a false read: it can only be
	// reached if some OTHER agent's captionarr work is live in the deployed
	// image and genuinely satisfied a request for this file, which is still
	// "past the transcode stage" and an acceptable outcome for this
	// assertion. Transcoding (caught mid-run) is the least likely of the
	// three to observe by the time this poll runs -- the job has already
	// reported Succeeded above -- but is accepted for the same reason.
	base, stopPF := portForwardService(ctx, t, "ui", uiServicePort)
	defer stopPF()
	pageClient := &http.Client{Timeout: 15 * time.Second}
	ref := types.NamespacedName{Namespace: Namespace, Name: movieName}
	waitFor(t, ctx, uiPageWaitTimeout, "GET /pipeline shows Movie "+movieName+" at or past the transcode stage",
		func(ctx context.Context) (bool, error) {
			body, status, err := httpGetString(ctx, pageClient, base+"/pipeline")
			if err != nil || status != http.StatusOK {
				//nolint:nilerr // keep polling; a port-forward hiccup is transient
				return false, nil
			}
			stage, ok := pipelineRowStage(body, commonv1.MediaKindMovie, ref)
			if !ok {
				return false, nil
			}
			t.Logf("pipeline row for %s: data-stage=%q", ref, stage)
			switch pipeline.Stage(stage) {
			case pipeline.StageTranscoding, pipeline.StageTranscodeDone, pipeline.StageComplete:
				return true, nil
			default:
				return false, nil
			}
		})
}

// TestTranscodeContainerChangeMovesTheFile proves gap-fix ruling R-11: a
// TranscodeProfile whose output container differs from the source's is
// planned and run, not skipped. Under replaceSource=true (the profile's
// default) the worker writes <stem>.<container> beside the source, retires
// the source into the recycle bin and reports the new path; catalogarr's
// MediaFile controller then takes over spec.path (task X5a's swap under a
// new name), so the library ends at the .mp4 and the catalog follows it.
//
// Until R-11 this shape was ruling R8's Skipped: the worker renamed its
// output OVER the source path, so an .mp4 profile against a .mkv source
// would have written mp4 data behind a .mkv name.
func TestTranscodeContainerChangeMovesTheFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()

	rf := newRootFolder(ctx, t, "e2e-tjcc-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	filePath := path.Join(rf.Spec.Path, transcodeContainerFolder, transcodeContainerFile)
	plantMedia(t, hostPath(filePath))
	runScan(ctx, t, rf, catalogv1alpha1.ScanModeFull)

	files := waitForMediaFileCount(ctx, t, rf.Spec.Path, 1)
	mfKey := client.ObjectKeyFromObject(&files[0])
	mf := waitForMediaFileProbed(ctx, t, mfKey)
	require.Equal(t, int32(720), mf.Spec.Quality.Resolution)
	require.Equal(t, commonv1.SourceWebDL, mf.Spec.Quality.Source)
	require.Equal(t, filePath, mf.Spec.Path)
	firstProbedAt := mf.Status.ProbedAt
	require.NotNil(t, firstProbedAt)

	cleanupUnlessFailed(t, func() {
		_ = k8sClient.Delete(context.Background(), &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: mf.Spec.MediaRef.Name, Namespace: Namespace},
		})
	})

	// mp4 output against a .mkv source: the container-change shape.
	newTranscodeProfile(ctx, t, "e2e-tjcc-profile", 720, commonv1.SourceWebDL, transcodev1alpha1.ContainerMP4)

	tj := waitForTranscodeJobForMediaFile(ctx, t, Namespace, mf.Name)
	tjKey := client.ObjectKeyFromObject(&tj)
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), &tj) })

	planned := waitForTranscodeJobPhaseAtLeast(ctx, t, tjKey, transcodeJobPlannedTimeout, transcodev1alpha1.TranscodeJobPhasePlanned)
	require.NotNil(t, planned.Status.Plan, "a container change is planned, not skipped (R-11)")
	cond := findCondition(planned.Status.Conditions, transcodev1alpha1.TranscodeJobConditionPlanned)
	require.NotNil(t, cond, "Planned condition must be set")
	require.Equal(t, metav1.ConditionTrue, cond.Status)
	require.Equal(t, transcodejobctrl.ReasonContainerChange, cond.Reason)

	succeeded := waitForTranscodeJobPhase(ctx, t, tjKey, transcodeJobSucceededTimeout, transcodev1alpha1.TranscodeJobPhaseSucceeded)
	require.NotNil(t, succeeded.Status.Result)
	wantOutput := strings.TrimSuffix(filePath, ".mkv") + ".mp4"
	require.Equal(t, wantOutput, succeeded.Status.Result.OutputPath,
		"replaceSource=true writes <stem>.<container> beside the source")

	// On disk: the .mp4 is there and the .mkv has been retired.
	waitFor(t, ctx, recycledOriginalTimeout, "the .mp4 output beside the retired source", func(context.Context) (bool, error) {
		_, outErr := os.Stat(hostPath(wantOutput))
		_, srcErr := os.Stat(hostPath(filePath))
		return outErr == nil && os.IsNotExist(srcErr), nil
	})

	// The catalog follows: catalogarr takes over spec.path in the same
	// apply as the rest of the swap.
	swapped := waitForMediaFileSwapIncorporated(ctx, t, mfKey, firstProbedAt.Time)
	require.Equal(t, wantOutput, swapped.Spec.Path, "the MediaFile must follow the file to its new container")
	require.Equal(t, "mp4", swapped.Status.MediaInfo.Container, "the re-probe must read the new container")
}

// TestTranscodeDolbyVisionSkipped documents, rather than exercises, Dolby
// Vision handling (ruling R1: a reject decision -- DolbyVisionMode=reject,
// or DolbyVisionMode=passthrough with no VBV limits set -- lands as phase
// Skipped with status.plan left unset, as any Skipped decision does).
//
// Dolby Vision cannot be synthesized with ffmpeg alone: it needs an RPU (a
// "DOVI configuration record" the encoder embeds), which only a tool like
// dovi_tool produces from a real Dolby Vision master. This was confirmed
// the hard way, on this exact box, before this task started --
// pkg/mediainfo/ffprobe_test.go's doviStreamsJSON fixture is HAND-AUTHORED
// JSON rather than a captured ffprobe run, and its own doc comment records
// that `ffmpeg -dolbyvision 1` on this box's ffmpeg refuses with "Dolby
// Vision requires VBV settings" and that there is no real RPU to feed it
// even if it did not. Adding dovi_tool is out of this task's scope (the
// brief says so explicitly), and faking a Dolby Vision stream well enough
// to fool pkg/mediainfo.ClassifyHDR's actual side-data walk (Dovi.Profile
// 5 or 7, or a BLSignalCompatibilityID of 1/2/4) would mean hand-crafting
// real HEVC SEI bytes -- not a fixture, a codec implementation.
//
// pkg/transcode.Plan's DecisionReject path and
// app/squash/controller/transcodejob's handling of it (ReasonRejected,
// phase Skipped, status.plan unset) are exercised by pkg/transcode's own
// unit and golden tests instead, against hand-authored MediaInfo, not a
// real file -- see pkg/transcode/plan_test.go.
func TestTranscodeDolbyVisionSkipped(t *testing.T) {
	t.Skip("Dolby Vision cannot be synthesized with ffmpeg alone: it needs an RPU " +
		"(e.g. from dovi_tool), which this task deliberately does not add. " +
		"pkg/mediainfo/ffprobe_test.go's doviStreamsJSON fixture already records that this box's " +
		"ffmpeg refuses -dolbyvision without a real master (\"Dolby Vision requires VBV settings\"), " +
		"and there is no real Dolby Vision RPU to encode even if it did not. Ruling R1 " +
		"(docs/superpowers/plans/2026-09-23-phase-e-transcode.md) and " +
		"app/squash/controller/transcodejob's DecisionReject handling are exercised instead by " +
		"pkg/transcode's own unit and golden tests, against hand-authored MediaInfo, not a real file.")
}

// TestDownloadScenario1TranscodeLeg is scenario 1's transcode leg
// ("MediaFile -> TranscodeJob Succeeded") through the REAL download-import
// route: a torrent grab against test/fixtures/seeder, a real import to a
// MediaFile, then a real TranscodeJob through Succeeded. It used to be a
// permanent skip: the download-import route could never reach a MediaFile
// at all, because test/fixtures/seeder always named its content
// "clustarr-fixture.bin", outside pkg/fsops.MediaExtensions -- see this
// file's git history for the former doc comment recording that in full.
// X12c (docs/superpowers/plans/2026-09-23-gap-fixes.md) closed the
// fixture-shape gap (seeder.ContentName's own doc comment); this now
// drives the real pipeline instead of documenting why it could not.
//
// It starts its OWN independent Movie/Download -- see
// TestDownloadScenario1SubtitleLeg's identical doc comment paragraph
// (subtitle_test.go) for why sharing download_test.go's
// TestDownloadTorrentGrabToImportAttempt's objects would be the wrong
// choice here, not an oversight.
//
// newTranscodeProfile is scoped to (1080, Bluray) -- reusing
// libraryscan_test.go's fixtureMovieFile's own (resolution, source), same
// as TestDownloadScenario1SubtitleLeg's SubtitleProfile and for the
// identical reason: see waitForImportOutcome's doc comment
// (helpers_test.go) for why the shared download fixture always parses to
// that quality, and why the reuse is safe under this suite's sequential,
// alphabetical-by-filename test ordering.
//
// This function also closes Phase H's scenario 1 trace check ("one
// trace_id appears in the logs of catalogarr, indexarr, grabarr and
// importarr for the same grab", remaining-work.md) -- HONESTLY scoped, not
// as written: see sharedTraceID's own doc comment (helpers_test.go) for
// why this suite's direct-create Download flow structurally never reaches
// catalogarr's search/grab worker or indexarr at all, so a real, full
// four-service trace needs a different scenario (a real Search-driven
// grab, matching indexer_test.go's scenario 17 shape) that no task has
// built yet. What this DOES prove for real: the bus hop between grabarr
// (publishing an ImportTask on Download completion) and importarr-worker
// (consuming it and creating the MediaFile) carries ONE shared
// Clustarr-Trace-derived trace_id, which is the one hop in this flow where
// propagation could silently break without a same-process call stack to
// carry it -- see sharedTraceID's own reasoning for why the other legs of
// this flow (grabarr's own watch-driven Download reconcile, catalogarr's
// watch-driven MediaFile/TranscodeJob reconciles) need no such proof:
// there is no message-queue hop there for propagation to fail across.
//
// TestTranscodeMediaFileThroughTranscodeJob (this file) is scenario 12's
// own, more thorough TranscodeJob leg, through the LibraryScan route.
func TestDownloadScenario1TranscodeLeg(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureSeederService)

	t0 := time.Now().Add(-time.Minute)

	rf := newRootFolder(ctx, t, "e2e-dl1tj-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	movie := newMovie(ctx, t, "e2e-dl1tj-movie", fixtureTmdbID, QualityProfileName, rf.Name, catalogv1alpha1.MinimumAvailabilityAnnounced)
	settled := waitForMovieSettled(ctx, t, movie, "Inception")

	newTorrentDownloadClientE2E(ctx, t, "e2e-dl1tj-dc")
	torrentURL := "http://" + fixtureSeederService + "." + Namespace + ".svc/fixture.torrent"
	dl := newTorrentDownloadE2E(ctx, t, "e2e-dl1tj-dl", &settled, torrentURL, "guid-dl1tj-1", QualityProfileName)
	waitForDownloadPhaseAtLeast(ctx, t, dl, downloadCompleteTimeout, downloadv1alpha1.DownloadPhaseCompleted)

	imp := waitForImportOutcome(ctx, t, dl, importAttemptTimeout)
	require.Len(t, imp.Imported, 1)
	mfKey := client.ObjectKey{Namespace: Namespace, Name: imp.Imported[0].MediaFileRef}
	mf := waitForMediaFileProbed(ctx, t, mfKey)
	require.Equal(t, "1080", mf.Labels[catalogv1alpha1.LabelResolution],
		"catalogarr's mirrored labels must carry the imported file's real, frozen-at-import resolution")
	require.Equal(t, "bluray", mf.Labels[catalogv1alpha1.LabelSource])

	newTranscodeProfile(ctx, t, "e2e-dl1tj-profile", 1080, commonv1.SourceBluray, transcodev1alpha1.ContainerMKV)

	tj := waitForTranscodeJobForMediaFile(ctx, t, Namespace, mf.Name)
	tjKey := client.ObjectKeyFromObject(&tj)
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), &tj) })

	planned := waitForTranscodeJobPhaseAtLeast(ctx, t, tjKey, transcodeJobPlannedTimeout, transcodev1alpha1.TranscodeJobPhasePlanned)
	require.NotNil(t, planned.Status.Plan)
	require.Equal(t, transcodev1alpha1.PlanModeTranscode, planned.Status.Plan.Mode,
		"the baked probe clip (h264/AAC) under the profile's hevc default must plan a real encode, not a skip or remux")

	succeeded := waitForTranscodeJobPhase(ctx, t, tjKey, transcodeJobSucceededTimeout, transcodev1alpha1.TranscodeJobPhaseSucceeded)
	require.NotNil(t, succeeded.Status.Result)
	require.NotEmpty(t, succeeded.Status.Result.OutputPath)
	require.NotZero(t, succeeded.Status.Result.OutputSizeBytes)

	// The trace check: see this function's own doc comment for exactly
	// what is and is not proven here.
	grabarrIDs := deploymentTraceIDsSince(ctx, t, "grabarr", t0)
	importarrIDs := deploymentTraceIDsSince(ctx, t, "importarr-worker", t0)
	shared := sharedTraceID(grabarrIDs, importarrIDs)
	require.NotEmptyf(t, shared,
		"no trace_id is common to grabarr's and importarr-worker's logs since %s -- "+
			"grabarr saw %d distinct trace_id(s), importarr-worker saw %d; Clustarr-Trace propagation "+
			"across the ImportTask bus hop (pkg/events hooks) may be broken, or the two Deployments' "+
			"logs for this specific grab were not both captured",
		t0.Format(time.RFC3339), len(grabarrIDs), len(importarrIDs))
}
