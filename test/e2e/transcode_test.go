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

// Task E-5 -- transcode fixtures and Phase H scenario 12 (M4, transcode).
//
// Since ADR-0016 the TranscodeJob kind is gone: a file's transcode -- its
// plan, admission, dispatch and swap -- is its own MediaFile's
// status.transcode, which every scenario here reads (describeTranscode,
// waitForTranscodePhase). The history below predates that fold; F8.9
// rewrites it in the test batch's T2, with scenario 12's main leg
// (TestTranscodeMediaFileThroughItsStatus; the TranscodeJob-shaped
// TestTranscodeMediaFileThroughTranscodeJob was deleted in T1).
//
// Phase E's code is complete and wired (E-4, 35c298e/b001001): transcode
// registers the TranscodeProfile and TranscodeJob controllers and the slot
// scheduler; config/manager/transcode.yaml (composed into config/e2e
// through config/default) ships the transcode ServiceAccount and a
// --slots cpu=2,nvidia=1,intel=1 budget. --worker-image
// (CLUSTARR_WORKER_IMAGE) points at the dedicated transcoder image (build:
// Dockerfile.transcoder; every pool runs it, nvidia's included, ADR-0015),
// not the media image, since the encoding runtime moved out of media; hack/e2e.sh's
// `make docker-build` builds and kind-loads it and waits on
// deployment/transcode's rollout. Nothing in config/e2e needed adding for
// this file's scenario to reach a real worker pod -- verified by reading
// config/manager/transcode.yaml, config/rbac/kustomization.yaml,
// config/default/kustomization.yaml and hack/e2e.sh's own WORKLOADS list,
// not assumed.
//
// X14 retired the per-Job `transcode-worker` ServiceAccount, ClusterRole
// and binding (config/rbac/transcode_worker_role*.yaml, and `clustarr
// transcode --role worker`) along with them: the transcode itself now runs
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
// task's brief, is the UNIT test
// pkg/mediainfo/ffprobeexec/hdr10_fixture_test.go -- a clip ffmpeg writes but
// pkg/mediainfo.ClassifyHDR reads as SDR proves nothing, so that test, not an
// e2e assertion, is what actually establishes the recipe works.
//
// Deliberately NOT reused here: the source this scenario needs is one the
// standard (pkg/transcode/standard, ffgo spec §1) decides to actually
// ENCODE, so the real worker path gets exercised. The suite's ordinary
// probe clip (test/fixtures/seed.ClipName) is H.264/AAC at 1920x1080, which
// the standard encodes to HEVC Main (8-bit: SDR at 1080p or less). The
// HDR10 clip, by contrast, IS already HEVC 10-bit -- what the standard
// writes for HDR -- so it would be skipped ("already HEVC Main 10"), not
// encoded. It is also video-only, a single lavfi source.
//
// # Dolby Vision
//
// Cannot be synthesized with ffmpeg alone -- see
// TestTranscodeDolbyVisionSkipped's own doc comment.
//
// # The standard, in-process (ffgo Phase 5)
//
// Since 2026-10-01 a TranscodeProfile names no encoder settings: it picks
// files, a container, a quality and the audio languages, and the standard
// decides the rest; the worker runs it in-process on FFmpeg 9's libraries,
// and the transcoder image carries no ffmpeg executable. This scenario
// asserts the standard's plan (engine ffgo, a plan hash), its 8-bit target,
// the CLUSTARR_PROFILE tag the output carries, and that a later profile
// edit -- a new status.hash -- queues nothing for the transcoded file.
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
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

const (
	// transcodeMovieFolder/File plant the file scenario 12's main flow
	// transcodes. The folder embeds the provider id in the form
	// pkg/release/ids.go recognises ("{tmdb-N}"), reusing fixtureTmdbID
	// (libraryscan_test.go) since it is the only id the in-cluster TMDB stub
	// can answer for -- though this scenario never waits on that metadata
	// (see the main test's own comment), only on the MediaFile.
	//
	// "1080p" + "HDTV" is chosen so pkg/release freezes
	// spec.quality.{resolution,source} = {1080, tv}: a signature no other
	// scenario in this package ever plants a REAL (plantMedia + full scan)
	// MediaFile with. The other real MediaFiles in test/e2e are
	// 1080p/bluray (fixtureMovieFolder/File, libraryscan_test.go),
	// 1080p/webrip (subtitle_test.go) and 1080p/webdl (below). Every one is
	// 1080p because the probe corrects a name's resolution
	// (quality.AugmentFromMediaInfo, in the rescan and the import alike)
	// and the one probe clip is 1920x1080 (test/fixtures/mkclips' ruling):
	// a "2160p" or "720p" name would freeze as 1080p and miss a selector
	// written for the name. Only the source can tell them apart.
	// series_test.go's 720p files use plantFiller, never probed into a
	// real MediaFile, so they cannot collide.
	transcodeMovieFolder = "Fixture Transcode Movie (2019) {tmdb-27205}"
	transcodeMovieFile   = "Fixture.Transcode.Movie.2019.1080p.HDTV.x264-CLUSTARR.mkv"

	// transcodeContainerFolder/File plant TestTranscodeContainerChangeMovesTheFile's
	// file: a distinct (1080, webdl) signature, same reasoning as above.
	transcodeContainerFolder = "Fixture Transcode Container Movie (2019) {tmdb-27205}"
	transcodeContainerFile   = "Fixture.Transcode.Container.Movie.2019.1080p.WEB-DL.x264-CLUSTARR.mkv"

	// defaultRecycleBinLogical mirrors RootFolderSpec.RecycleBin.Path's own
	// CRD default (api/catalog/v1alpha1/rootfolder_types.go) and
	// app/transcode/worker/paths.go's identical code-level floor
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
	// redelivery ladder -- app/transcode/controller/transcodeprofile watches
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
	// mirroring engineReadyTimeout's own reasoning for grab's engine
	// StatefulSet, since the worker image may not already be warm on the
	// node), admission against the slot budget (config/manager/
	// transcode.yaml ships --slots cpu=2,..., so this scenario's one job is
	// admitted on the very first admission pass, never queued behind
	// another scenario -- transcode is the only component that creates
	// TranscodeJobs, and this file's two scenarios are the only ones that
	// create TranscodeProfiles), then transcode's worker actually running
	// the in-process engine (libx265 at the standard's CPU settings) against the
	// suite's ten-second 1080p probe clip, verifying the output and
	// swapping it over the source (R5: hard-link the original into the
	// recycle bin, then rename the output over the source path). A
	// ten-second 1080p test pattern encodes in a minute or two at the
	// standard's x265 "slow" preset on a real node; this is sized for a
	// loaded single kind node and a cold image pull as well.
	transcodeJobSucceededTimeout = 10 * time.Minute

	// recycledOriginalTimeout bounds the wait for the pre-transcode
	// original to appear in the recycle bin. It is not additive with
	// transcodeJobSucceededTimeout's own wait: the worker's swap happens
	// strictly before the Job exits 0 (R5's ordering), so by the time
	// TranscodeJob reports Succeeded the recycled file already exists on
	// disk; this only covers the hostPath mount and this process's own
	// os.Stat catching up.
	recycledOriginalTimeout = 2 * time.Minute

	// transcodeSwapIncorporatedTimeout covers catalog's MediaFile
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
	// let its pods go (app/transcode/controller/pool.Mutable's own gate).
	poolSuspendedTimeout = 2 * time.Minute
)

// newTranscodeProfile creates a cluster-scoped TranscodeProfile that selects
// MediaFiles by an EXACT (resolution, source) label pair --
// catalog.clustarr.io/resolution and catalog.clustarr.io/source, which
// app/catalog/controller/mediafile/labels.go's mirrorLabels sets from the
// frozen-at-import Quality every real MediaFile in this suite carries -- and
// waits for it to reach Ready with a non-empty status.hash (the controller's
// own gate before it will plan or tag anything against this profile:
// app/transcode/controller/transcodejob/controller.go's plan() waits on exactly
// this field too).
//
// container overrides the CRD's own "mkv" default, and policy.minDuration
// is 0s: the CRD's 1m default would skip the suite's ten-second clip as an
// extra. Every other field is left at its Go zero value, relying on the
// real apiserver's structural-schema defaulting for every leaf field that
// has one (the rest of Policy) -- the same reliance
// newRootFolder/newIndexer/newMovie already place on CRD defaults
// throughout this suite for a typed client Create against a REAL apiserver
// (kind's, here; envtest's elsewhere), as opposed to a fake client, which
// applies no defaulting at all. The three profile fields whose Go zero
// value never marshals as genuinely absent (ActiveDeadline, Resources,
// Scratch -- metav1.Duration and resource.Quantity both always emit a
// quoted value, never omit) are floored in Go by transcode's own buildJob
// (app/transcode/controller/transcodejob/job.go's activeDeadlineFor/
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
			Policy:    transcodev1alpha1.PolicySpec{MinDuration: &metav1.Duration{}},
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

// describeTranscode renders a MediaFile's status.transcode for a failure
// message: the transcode state lives on the file since ADR-0016 folded the
// TranscodeJob kind into it (F8.9).
func describeTranscode(key client.ObjectKey) func() string {
	return func() string {
		var mf catalogv1alpha1.MediaFile
		if err := k8sClient.Get(context.Background(), key, &mf); err != nil {
			return fmt.Sprintf("MediaFile %s could not be read back: %v", key.Name, err)
		}
		tr := mf.Status.Transcode
		if tr == nil {
			return fmt.Sprintf("MediaFile %s has no status.transcode (spec.path=%s)", key.Name, mf.Spec.Path)
		}
		seq := int64(0)
		if tr.Dispatch != nil {
			seq = tr.Dispatch.Seq
		}
		return fmt.Sprintf("MediaFile %s transcode phase=%q reason=%q message=%q profile=%s@%s class=%s pool=%s worker=%s seq=%d attempts=%d suspended=%t path=%s",
			key.Name, tr.Phase, tr.Reason, tr.Message, tr.Profile, tr.ProfileHash, tr.Class, tr.Pool, tr.WorkerPod, seq, tr.Attempts, tr.Suspended, mf.Spec.Path)
	}
}

// waitForTranscodePhase polls until status.transcode.phase is one of want.
func waitForTranscodePhase(ctx context.Context, t *testing.T, key client.ObjectKey, timeout time.Duration, want ...catalogv1alpha1.TranscodePhase) catalogv1alpha1.MediaFile {
	t.Helper()
	var live catalogv1alpha1.MediaFile
	waitFor(t, ctx, timeout, fmt.Sprintf("MediaFile %s status.transcode.phase in %v", key.Name, want), func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, key, &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return live.Status.Transcode != nil && slices.Contains(want, live.Status.Transcode.Phase), nil
	}, describeTranscode(key))
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

// TestTranscodeContainerChangeMovesTheFile proves gap-fix ruling R-11: a
// TranscodeProfile whose output container differs from the source's is
// planned and run, not skipped. Under replaceSource=true (the profile's
// default) the worker writes <stem>.<container> beside the source, retires
// the source into the recycle bin and reports the new path; catalog's
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
	require.Equal(t, int32(1080), mf.Spec.Quality.Resolution)
	require.Equal(t, commonv1.SourceWebDL, mf.Spec.Quality.Source)
	require.Equal(t, filePath, mf.Spec.Path)
	firstProbedAt := mf.Status.ProbedAt
	require.NotNil(t, firstProbedAt)

	cleanupUnlessFailed(t, func() {
		_ = k8sClient.Delete(context.Background(), &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: mf.Spec.MediaRef.Name, Namespace: Namespace},
		})
	})

	// mp4 output against a .mkv source: the container-change shape. The
	// file's own status.transcode carries the plan and the swap since
	// ADR-0016 (F8.9).
	newTranscodeProfile(ctx, t, "e2e-tjcc-profile", 1080, commonv1.SourceWebDL, transcodev1alpha1.ContainerMP4)
	done := waitForTranscodePhase(ctx, t, mfKey, transcodeJobSucceededTimeout+transcodeSwapIncorporatedTimeout, catalogv1alpha1.TranscodePhaseSucceeded)
	wantOutput := strings.TrimSuffix(filePath, ".mkv") + ".mp4"
	require.Equal(t, wantOutput, done.Spec.Path, "the loop takes spec.path over to the .mp4 (loop spec §3.10)")
	require.Equal(t, "mp4", done.Status.MediaInfo.Container, "the re-probe must read the new container")
	require.NotNil(t, done.Status.ProbedAt)
	require.True(t, done.Status.ProbedAt.After(firstProbedAt.Time), "the swap is re-probed")

	// On disk: the .mp4 is there and the .mkv has been retired.
	waitFor(t, ctx, recycledOriginalTimeout, "the .mp4 output beside the retired source", func(context.Context) (bool, error) {
		_, outErr := os.Stat(hostPath(wantOutput))
		_, srcErr := os.Stat(hostPath(filePath))
		return outErr == nil && os.IsNotExist(srcErr), nil
	})
	require.Nil(t, done.Status.Transcode.Result, "the swap is incorporated: the result is not kept for a replaced source")
}

// TestTranscodeDolbyVisionSkipped documents, rather than exercises, Dolby
// Vision handling. Under the standard (ffgo spec §1) Dolby Vision 7 and
// 8.1 are encoded as HDR10 from their base layer, with the RPU dropped; a
// profile has no Dolby Vision setting any more.
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
// The standard's Dolby Vision plans are exercised by
// pkg/transcode/standard's unit tests against hand-authored MediaInfo, by
// app/transcode/controller/transcodejob's TestTheStandardEncodesDolbyVisionOnTheProfilesGPU,
// and by the parity harness (test/parity) over real Dolby Vision clips from
// the owner's library.
func TestTranscodeDolbyVisionSkipped(t *testing.T) {
	t.Skip("Dolby Vision cannot be synthesized with ffmpeg alone: it needs an RPU " +
		"(e.g. from dovi_tool), which this task deliberately does not add. " +
		"pkg/mediainfo/ffprobe_test.go's doviStreamsJSON fixture already records that this box's " +
		"ffmpeg refuses -dolbyvision without a real master (\"Dolby Vision requires VBV settings\"), " +
		"and there is no real Dolby Vision RPU to encode even if it did not. The standard's " +
		"Dolby Vision plans are exercised instead by pkg/transcode/standard's unit tests and by the " +
		"parity harness (test/parity) over real clips.")
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
// trace_id appears in the logs of catalog, index, grab and
// import for the same grab", remaining-work.md) -- HONESTLY scoped, not
// as written: see sharedTraceID's own doc comment (helpers_test.go) for
// why this suite's direct-create Download flow structurally never reaches
// catalog's search/grab worker or index at all, so a real, full
// four-service trace needs a different scenario (a real Search-driven
// grab, matching indexer_test.go's scenario 17 shape) that no task has
// built yet. What this DOES prove for real: the bus hop between grab
// (publishing an ImportTask on Download completion) and import-worker
// (consuming it and creating the MediaFile) carries ONE shared
// Clustarr-Trace-derived trace_id, which is the one hop in this flow where
// propagation could silently break without a same-process call stack to
// carry it -- see sharedTraceID's own reasoning for why the other legs of
// this flow (grab's own watch-driven Download reconcile, catalog's
// watch-driven MediaFile/TranscodeJob reconciles) need no such proof:
// there is no message-queue hop there for propagation to fail across.
//
// TestTranscodeMediaFileThroughTranscodeJob (this file) is scenario 12's
// own, more thorough TranscodeJob leg, through the LibraryScan route.
func TestDownloadScenario1TranscodeLeg(t *testing.T) {
	// The Download kind is gone (A9.1): a grab is a status.downloads[] entry on its item,
	// started by a Search with spec.grab naming the fixture release (spec §6.13), and the
	// transcode state is the MediaFile's status.transcode (ADR-0016). The body this leg had,
	// with its grab-to-import trace check, is at the T1 base,
	// git show 10f1a491:test/e2e/transcode_test.go; F8.9 rewrites it in T2.
	t.Skip("T2: scenario 1's transcode leg starts from a Search pick and reads status.transcode (A8.4, F8.9)")
}
