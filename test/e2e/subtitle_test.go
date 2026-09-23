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

// Task F-7 -- mock subtitle-provider fixtures and Phase H scenario 13 (M5,
// captionarr). Written and NEVER EXECUTED, per this task's explicit
// instruction: no `make e2e`, no `hack/e2e.sh`, no `go test -tags e2e
// ./test/e2e/...` against a kind cluster. `go vet -tags e2e` and
// golangci-lint were run against this file; nothing more.
//
// # Wiring status
//
// F-0 through F-6 are all committed as of this writing (2026-09-23):
// captionarr/run.go's setupControllers registers subtitleprofile,
// subtitleprovider and subtitlerequest, and setupWorkers registers the
// fetch worker on both consumers (confirmed by reading captionarr/run.go
// at HEAD, not assumed -- F-6 landed mid-way through this task, after this
// package doc comment's first draft said otherwise). Every scenario below
// is written against a real, wired reconcile loop, the same posture
// indexer_test.go's scenario 17 was written in once D1-10 had landed its
// own wiring -- it has simply never been PROVEN by actually running it,
// which is this task's own explicit instruction, not a gap in the code.
//
// # Fixtures
//
// test/fixtures/opensubtitlesstub and test/fixtures/gestdownstub mock the
// two remote providers Phase F ships real clients for (embedded is local
// and needs no upstream, subdl/subsource/whisper have no client at all,
// ruling R5). Deployed from config/e2e/opensubtitles-stub.yaml and
// config/e2e/gestdown-stub.yaml. Both packages carry their own guard-rail
// unit tests proving their canned JSON/SRT round-trips through the REAL
// pkg/subtitles/providers/{opensubtitlescom,gestdown} client packages --
// see their server_test.go files -- so a typo in a field name fails `go
// test` rather than surfacing as an unexplained decode error deep into a
// kind run.
//
// # Why the throttle/fallthrough proof needs an Episode, not a Movie
//
// The task brief calls this "the behaviour most worth an e2e in the whole
// phase", so it is worth stating precisely why it cannot be built against
// an ordinary movie:
//
//   - captionarr/worker/fetch/worker.go's eligible() registers each
//     provider into a pkg/subtitles.Registry keyed by Name(), and
//     Registry.Register returns ErrDuplicateProvider for a second provider
//     of the same type -- "one account per provider", Bazarr's own model
//     (eligible's own doc comment). Two SubtitleProviders of the SAME type
//     can therefore never both be searched for one task, so a scenario
//     built from two opensubtitlescom SubtitleProviders could never prove
//     a search falling through from one to a DIFFERENT one that still
//     answers.
//   - pkg/subtitles/providers/gestdown.Provider.Capabilities() reports only
//     Episodes: true (confirmed against provider.go); pkg/subtitles/
//     registry.go's Registry.For(kind) filters on exactly that field, and
//     captionarr/worker/fetch/select.go's searchable() independently hard-
//     codes "gestdown is episode-only, and only with a tvdb id". Gestdown
//     is therefore never even offered a Movie search.
//
// The only real two-different-types fallthrough this codebase's Phase F
// can exercise is OpenSubtitles.com (Movies+Episodes) throttled, with
// Gestdown (Episodes only) as the surviving second provider -- which needs
// an Episode-kind MediaFile.
//
// TestSubtitleThrottleFallsThroughToGestdown reaches that MediaFile by
// creating it directly rather than through a real LibraryScan, for a
// structural reason series_test.go's own
// TestSeriesRootFolderScanIsNotSupportedYet already pins:
// importarr/worker/rescan/mediafile.go's handleMediaFile short-circuits
// every file under a non-movie root folder into
// LibraryScan.status.unmatched with CodeUnsupportedKind -- series
// root-folder scanning is M6 work, not this task's, so there is no real
// path to an Episode MediaFile today. This is the same category of
// permanent, named gap helpers_test.go's importGapReason documents for the
// download fixtures' ".bin" extension. The direct-create is the same
// technique this package's own newTorrentDownloadE2E/newUsenetDownloadE2E
// and test/e2e/ui_test.go's newUIDownload already use to stand in for a
// real decision this suite structurally cannot reach yet: everything AFTER
// the MediaFile exists -- catalogarr's real ffprobe, the real
// SubtitleProfile/SubtitleRequest controllers, the real fetch worker, the
// real (mocked) providers -- runs for real, against a real Series and
// Episode created through catalogarr's own real controllers
// (series_test.go's createSeries/waitForSeriesReady/requireEpisode).
//
// # Scenario 1's subtitle leg
//
// TestDownloadScenario1SubtitleLegBlocked mirrors
// transcode_test.go's TestDownloadScenario1TranscodeLegBlocked: scenario
// 1's download-import route never reaches a MediaFile at all
// (helpers_test.go's importGapReason), so there is nothing for captionarr
// to attach a SubtitleRequest to. Checked at HEAD, not assumed: pkg/fsops.
// MediaExtensions (classify.go) is still exactly
// {.mkv,.mp4,.m4v,.avi,.mov,.wmv,.ts,.m2ts,.mpg,.mpeg,.webm} for video plus
// the audio/book/comic sets, and neither test/fixtures/seeder nor
// test/fixtures/nntpstub names its content anything but
// "clustarr-fixture.bin".
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/test/fixtures/gestdownstub"
	"github.com/mediactl/clustarr/test/fixtures/opensubtitlesstub"
)

const (
	// subtitleMovieFolder/File plant scenario 13's movie leg. The
	// resolution+source pair (1080p/WEBRip) is chosen so pkg/release
	// freezes a Quality signature no OTHER e2e scenario's real, PROBED
	// MediaFile uses -- confirmed by grepping this package for
	// "1080p\|720p\|2160p\|480p\|BluRay\|WEB-DL\|WEBRip\|HDTV\|DVDRip"
	// before choosing these tokens (transcode_test.go's own
	// transcodeMovieFolder doc comment sets this precedent): 1080p/bluray
	// is fixtureMovieFolder (libraryscan_test.go, the most common), 2160p/
	// webdl is transcodeMovieFolder, 720p/webdl is
	// transcodeContainerFolder. series_test.go's 720p files use
	// plantFiller, never probed into a real MediaFile with real labels, so
	// they cannot collide either. Reusing fixtureTmdbID (27205, Inception)
	// is the same choice transcode_test.go's transcodeMovieFolder makes --
	// a real, TMDB-stub-answerable id every scenario may safely share,
	// since a rescan upserts one Movie per tmdb id rather than forking one
	// per scenario.
	subtitleMovieFolder = "Fixture Subtitle Movie (2019) {tmdb-27205}"
	subtitleMovieFile   = opensubtitlesstub.FixtureReleaseInfo + ".mkv"

	// subtitleProfileReadyTimeout mirrors newRankedQualityProfile's and
	// squasharr's newTranscodeProfile's identical 2-minute budget for a
	// cluster-scoped profile object's own reconcile.
	subtitleProfileReadyTimeout = 2 * time.Minute

	// subtitleFetchTimeout covers one fetch task's real round trip. The
	// SubtitleRequest controller's own reconcile is watch-driven (no
	// queue, no redelivery ladder) and publishes on the SAME pass that
	// creates the item, so the worker's FIRST delivery attempt on
	// ConsumerCaptionFetchHigh/-Normal (AckWait 90s, pkg/events/
	// topology.go) is the case this covers: a login, a search and a
	// download against two in-cluster HTTP mocks, every round trip
	// sub-second. It does not try to reach that consumer's full
	// 8-attempt/30s-2m-10m-1h-6h backoff ladder -- a first delivery that
	// never arrives at all is a broken cluster (the same reasoning
	// scenarioTimeout's own comment gives), not something worth budgeting
	// for here.
	subtitleFetchTimeout = 3 * time.Minute
)

// subtitleItem returns the item for langKey, or nil.
func subtitleItem(items []subtitlev1alpha1.SubtitleItem, langKey string) *subtitlev1alpha1.SubtitleItem {
	for i := range items {
		if items[i].LangKey == langKey {
			return &items[i]
		}
	}
	return nil
}

// describeSubtitleRequest renders one SubtitleRequest's phase, items and
// conditions for a failure message, mirroring describeSearch/describeIndexer.
func describeSubtitleRequest(key client.ObjectKey) func() string {
	return func() string {
		var live subtitlev1alpha1.SubtitleRequest
		if err := k8sClient.Get(context.Background(), key, &live); err != nil {
			return fmt.Sprintf("SubtitleRequest %s could not be read back: %v", key.Name, err)
		}
		out := fmt.Sprintf("SubtitleRequest %s phase=%q profileGeneration=%d probeHash=%q",
			key.Name, live.Status.Phase, live.Status.ProfileGeneration, live.Status.ProbeHash)
		for _, it := range live.Status.Items {
			out += fmt.Sprintf("\n    item langKey=%q state=%q score=%d/%d provider=%q path=%q lastError=%q",
				it.LangKey, it.State, it.Score, it.ScoreOutOf, it.Provider, it.Path, it.LastError)
		}
		for _, c := range live.Status.Conditions {
			out += fmt.Sprintf("\n    condition %s=%s reason=%s message=%q", c.Type, c.Status, c.Reason, c.Message)
		}
		return out
	}
}

// waitForSubtitleProfileReady creates nothing; it polls an already-created
// SubtitleProfile until it reports Ready, mirroring
// newRankedQualityProfile's wait.
func waitForSubtitleProfileReady(ctx context.Context, t *testing.T, name string) subtitlev1alpha1.SubtitleProfile {
	t.Helper()
	var live subtitlev1alpha1.SubtitleProfile
	waitFor(t, ctx, subtitleProfileReadyTimeout, "SubtitleProfile "+name+" Ready", func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return isConditionTrue(live.Status.Conditions, subtitlev1alpha1.SubtitleProfileConditionReady), nil
	})
	return live
}

// setOpenSubtitlesMode drives opensubtitles-stub's control route (see that
// package's doc comment, "The control route") over a port-forwarded tunnel
// -- the only way this test process can reach a ClusterIP Service directly
// (portForwardService's own doc comment).
func setOpenSubtitlesMode(ctx context.Context, t *testing.T, mode opensubtitlesstub.Mode) {
	t.Helper()
	base, stop := portForwardService(ctx, t, "opensubtitles-stub", 80)
	defer stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/__mode__?mode="+string(mode), nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "setOpenSubtitlesMode(%s)", mode)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "setOpenSubtitlesMode(%s)", mode)
}

// readJSONLSince reads a fixture's JSONL request log (the same bridge
// readTorznabRequests uses: the test process cannot reach a ClusterIP
// Service's HTTP port directly, but every fixture in this suite writes its
// request log onto the shared /data volume this process CAN read), keeping
// only entries at or after t0. A missing file (the fixture has not been
// asked anything yet) is not an error.
func readJSONLSince[T any](logPath string, t0 time.Time, at func(T) time.Time) ([]T, error) {
	f, err := os.Open(logPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", logPath, err)
	}
	defer func() { _ = f.Close() }()

	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e T
		if err := json.Unmarshal(line, &e); err != nil {
			continue // a torn final line is expected; see readTorznabRequests
		}
		if !at(e).Before(t0) {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

func openSubtitlesRequestLogPath() string {
	return filepath.Join(dataDir(), fixtureDirName, "opensubtitles", "requests.jsonl")
}

func gestdownRequestLogPath() string {
	return filepath.Join(dataDir(), fixtureDirName, "gestdown", "requests.jsonl")
}

func readOpenSubtitlesRequestsSince(t *testing.T, t0 time.Time) []opensubtitlesstub.Entry {
	t.Helper()
	es, err := readJSONLSince[opensubtitlesstub.Entry](openSubtitlesRequestLogPath(), t0, func(e opensubtitlesstub.Entry) time.Time { return e.At })
	require.NoError(t, err)
	return es
}

func readGestdownRequestsSince(t *testing.T, t0 time.Time) []gestdownstub.Entry {
	t.Helper()
	es, err := readJSONLSince[gestdownstub.Entry](gestdownRequestLogPath(), t0, func(e gestdownstub.Entry) time.Time { return e.At })
	require.NoError(t, err)
	return es
}

// ---------------------------------------------------------------------------
// Scenario 13, main leg: a movie MediaFile through SubtitleRequest, a real
// downloaded sidecar, the pipeline page's SubtitleDone stage, and the
// item-liveness withdraw a profile-language removal drives.
// ---------------------------------------------------------------------------

// TestSubtitleRequestSidecarPipelineAndLanguageRemoval is scenario 13's core
// proof: a real, real-scanned video MediaFile gets exactly one
// SubtitleRequest; two wanted languages are fetched from the mocked
// OpenSubtitles.com provider; each sidecar lands next to the media file
// under pkg/subtitles.SidecarName's own name; catalogarr surfaces both on
// MediaFile.status.sidecars; the UI's pipeline page shows the subtitle
// stage by data-stage (D3 ruling R8); and removing one language from the
// profile withdraws exactly its item (and its mirrored sidecar) while the
// other survives -- the item-liveness protocol F-4/F-5 built
// (captionarr/status.IsLive), which CLAUDE.md's own "Gotchas" section
// names as the part of Phase F most likely to regress.
func TestSubtitleRequestSidecarPipelineAndLanguageRemoval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()

	setOpenSubtitlesMode(ctx, t, opensubtitlesstub.ModeOK)
	t.Cleanup(func() { setOpenSubtitlesMode(context.Background(), t, opensubtitlesstub.ModeOK) })

	rf := newRootFolder(ctx, t, "e2e-sub13-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	filePath := path.Join(rf.Spec.Path, subtitleMovieFolder, subtitleMovieFile)
	plantMedia(t, hostPath(filePath))
	runScan(ctx, t, rf, catalogv1alpha1.ScanModeFull)

	files := waitForMediaFileCount(ctx, t, rf.Spec.Path, 1)
	mf := waitForMediaFileProbed(ctx, t, client.ObjectKeyFromObject(&files[0]))
	movie := requireMovie(ctx, t, mf.Spec.MediaRef.Name)
	cleanupUnlessFailed(t, func() {
		_ = k8sClient.Delete(context.Background(), &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: movie.Name, Namespace: Namespace},
		})
	})
	require.Equal(t, "1080", mf.Labels[catalogv1alpha1.LabelResolution], "catalogarr's mirrored labels must carry the planted file's real resolution")
	require.Equal(t, "webrip", mf.Labels[catalogv1alpha1.LabelSource])

	provider := &subtitlev1alpha1.SubtitleProvider{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-sub13-os"), Namespace: Namespace},
		Spec: subtitlev1alpha1.SubtitleProviderSpec{
			Type: subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, Enabled: true, Priority: 10,
			SecretRef: &corev1.LocalObjectReference{Name: "opensubtitles-fixture-credentials"},
			Endpoint:  ptr.To("http://opensubtitles-stub." + Namespace + ".svc"),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, provider))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), provider) })

	profile := &subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-sub13-profile")},
		Spec: subtitlev1alpha1.SubtitleProfileSpec{
			// Scoped to exactly this scenario's own MediaFile signature --
			// see subtitleMovieFolder's doc comment. spec.default is
			// deliberately never used here: SubtitleProfile is
			// cluster-scoped, and a default profile would claim every
			// OTHER scenario's video MediaFile in the shared cluster for
			// as long as this profile lives (the same reasoning
			// transcode_test.go's package doc comment gives for its own
			// newTranscodeProfile).
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
				catalogv1alpha1.LabelKind:       string(commonv1.MediaKindMovie),
				catalogv1alpha1.LabelResolution: "1080",
				catalogv1alpha1.LabelSource:     "webrip",
			}},
			Languages: []subtitlev1alpha1.LanguageItem{
				{Key: "en", Language: "en"},
				{Key: "es", Language: "es"},
			},
			// A generous floor rather than one tuned to Bazarr's exact
			// weight table: pkg/subtitles/score_test.go already proves the
			// scoring arithmetic itself. This scenario's job is the
			// plumbing around it (request -> fetch -> sidecar -> status),
			// not re-deriving a score to the point.
			MinScorePercent: subtitlev1alpha1.ScorePct{Movie: 10, Episode: 10},
			Providers:       []string{provider.Name},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, profile))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), profile) })
	waitForSubtitleProfileReady(ctx, t, profile.Name)

	reqKey := client.ObjectKey{Namespace: Namespace, Name: mf.Name}

	// Exactly one SubtitleRequest for this MediaFile -- the deterministic
	// name/owner ensureSubtitleRequest uses makes a duplicate structurally
	// impossible, but listing rather than trusting that is what actually
	// proves it from the apiserver's own state.
	waitFor(t, ctx, subtitleFetchTimeout, "exactly one SubtitleRequest for MediaFile "+mf.Name,
		func(ctx context.Context) (bool, error) {
			var list subtitlev1alpha1.SubtitleRequestList
			if err := k8sClient.List(ctx, &list, client.InNamespace(Namespace)); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			n := 0
			for _, sr := range list.Items {
				if sr.Spec.MediaFileRef == mf.Name {
					n++
				}
			}
			return n == 1, nil
		})
	cleanupUnlessFailed(t, func() {
		_ = k8sClient.Delete(context.Background(), &subtitlev1alpha1.SubtitleRequest{
			ObjectMeta: metav1.ObjectMeta{Name: mf.Name, Namespace: Namespace},
		})
	})

	// Both wanted languages fetched from the mocked provider and written to
	// disk.
	waitFor(t, ctx, subtitleFetchTimeout, "SubtitleRequest "+mf.Name+" en and es both downloaded",
		func(ctx context.Context) (bool, error) {
			var live subtitlev1alpha1.SubtitleRequest
			if err := k8sClient.Get(ctx, reqKey, &live); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			en, es := subtitleItem(live.Status.Items, "en"), subtitleItem(live.Status.Items, "es")
			return itemDownloaded(en) && itemDownloaded(es), nil
		}, describeSubtitleRequest(reqKey))

	var withBoth subtitlev1alpha1.SubtitleRequest
	require.NoError(t, k8sClient.Get(ctx, reqKey, &withBoth))
	en := subtitleItem(withBoth.Status.Items, "en")
	require.NotNil(t, en)
	require.Equal(t, provider.Name, en.Provider, "the mocked OpenSubtitles.com provider must be the one credited")

	// The sidecar's on-disk name is pkg/subtitles.SidecarName's own
	// contract, not a guess: <stem>.<lang>.srt for a non-forced, non-HI
	// item (the mock never marks its candidate hi/forced).
	wantEnPath := subtitles.SidecarName(mf.Spec.Path, "en", string(profile.Spec.HIExtension))
	require.FileExists(t, hostPath(wantEnPath), "the en sidecar must exist under SidecarName's own path")
	require.Equal(t, filepath.Base(wantEnPath), en.Path, "status.items[].path is relative to the media file's directory")

	// catalogarr's own half: MediaFile.status.sidecars mirrors both
	// downloaded items (sidecars.go, inside the same status apply that
	// wrote status.mediaInfo).
	waitFor(t, ctx, 2*time.Minute, "MediaFile "+mf.Name+" status.sidecars carries both en and es",
		func(ctx context.Context) (bool, error) {
			var live catalogv1alpha1.MediaFile
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(&mf), &live); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			return len(live.Status.Sidecars) == 2, nil
		}, describeMediaFile(client.ObjectKeyFromObject(&mf)))

	t.Run("pipeline page shows the subtitle stage", func(t *testing.T) {
		base, stop := portForwardService(ctx, t, "ui", uiServicePort)
		defer stop()
		pageClient := &http.Client{Timeout: 15 * time.Second}

		// Title is recomputed fresh on every poll, from the same live read
		// the assertion checks against -- ui_test.go's own
		// TestUIPipelineAndDownloadsPages documents exactly why: it closes
		// the race between "the title we expected" and whatever the
		// metadata gateway had actually settled on TMDB tmdb-27205 by the
		// time this runs.
		waitFor(t, ctx, uiPageWaitTimeout, "GET /pipeline shows SubtitleDone for Movie "+movie.Name,
			func(ctx context.Context) (bool, error) {
				var live catalogv1alpha1.Movie
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(&movie), &live); err != nil {
					//nolint:nilerr // keep polling
					return false, nil
				}
				title := live.Name
				if live.Status.Metadata != nil && live.Status.Metadata.Title != "" {
					title = live.Status.Metadata.Title
				}
				body, status, err := httpGetString(ctx, pageClient, base+"/pipeline")
				if err != nil || status != http.StatusOK {
					//nolint:nilerr // keep polling; a port-forward hiccup is transient
					return false, nil
				}
				return strings.Contains(body, `data-stage="SubtitleDone"`) && strings.Contains(body, title), nil
			}, describeMovie(client.ObjectKeyFromObject(&movie)))
	})

	t.Run("removing a language from the profile withdraws exactly its item", func(t *testing.T) {
		var live subtitlev1alpha1.SubtitleProfile
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: profile.Name}, &live))
		patch := client.MergeFrom(live.DeepCopy())
		live.Spec.Languages = []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}}
		require.NoError(t, k8sClient.Patch(ctx, &live, patch))

		waitFor(t, ctx, subtitleFetchTimeout, "SubtitleRequest "+mf.Name+" withdrew the es item",
			func(ctx context.Context) (bool, error) {
				var req subtitlev1alpha1.SubtitleRequest
				if err := k8sClient.Get(ctx, reqKey, &req); err != nil {
					//nolint:nilerr // keep polling
					return false, nil
				}
				return subtitleItem(req.Status.Items, "es") == nil && subtitleItem(req.Status.Items, "en") != nil, nil
			}, describeSubtitleRequest(reqKey))

		var afterWithdraw subtitlev1alpha1.SubtitleRequest
		require.NoError(t, k8sClient.Get(ctx, reqKey, &afterWithdraw))
		require.Len(t, afterWithdraw.Status.Items, 1, "only the en item may remain")
		require.Equal(t, subtitlev1alpha1.SubtitleItemDownloaded, afterWithdraw.Status.Items[0].State,
			"withdrawing es must not disturb en's own downloaded state")

		// The withdraw propagates through catalogarr's own mirror too --
		// sidecarsFromSubtitleRequest only ever mirrors items the
		// SubtitleRequest still carries.
		waitFor(t, ctx, 2*time.Minute, "MediaFile "+mf.Name+" status.sidecars drops es",
			func(ctx context.Context) (bool, error) {
				var live catalogv1alpha1.MediaFile
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(&mf), &live); err != nil {
					//nolint:nilerr // keep polling
					return false, nil
				}
				return len(live.Status.Sidecars) == 1, nil
			}, describeMediaFile(client.ObjectKeyFromObject(&mf)))
	})
}

// itemDownloaded reports whether it is present, downloaded and carries a
// sidecar path -- the shape hasSubtitle (captionarr/worker/fetch/worker.go)
// itself checks before catalogarr's mirror will ever surface it.
func itemDownloaded(it *subtitlev1alpha1.SubtitleItem) bool {
	return it != nil && it.State == subtitlev1alpha1.SubtitleItemDownloaded && it.Path != ""
}

// ---------------------------------------------------------------------------
// Scenario 13, throttle leg: OpenSubtitles.com throttled -> Gestdown
// fallthrough, against a directly-created Episode MediaFile.
// ---------------------------------------------------------------------------

// TestSubtitleThrottleFallsThroughToGestdown is scenario 13's throttle
// proof: with opensubtitles-stub set to ModeThrottled (429 on every
// /subtitles and /download), the fetch worker's shared KV throttle
// (captionarr/throttle) must bench that SubtitleProvider -- surfaced on
// SubtitleProvider.status.conditions[Throttled] (ruling R2, the
// SubtitleProvider controller's own projection) -- and fall through to
// Gestdown, which must succeed. See this file's package doc comment,
// "Why the throttle/fallthrough proof needs an Episode, not a Movie", for
// why this cannot be built from a Movie, and "the throttle/fallthrough
// proof" section for why the MediaFile is created directly rather than
// through a LibraryScan.
func TestSubtitleThrottleFallsThroughToGestdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	t0 := time.Now()

	rf := newRootFolder(ctx, t, "e2e-sub13-thr-rf", catalogv1alpha1.RootFolderKindSeries, "tv")
	series := createSeries(ctx, t, rf.Name, 121361, catalogv1alpha1.SeriesTypeStandard)
	seriesLive := waitForSeriesReady(ctx, t, series, fixtureEpisodesPerSeries)
	episode := requireEpisode(ctx, t, series.Name+"-s01e01")

	// Direct-create: see the package doc comment. Everything from here on
	// -- the probe, the profile/request controllers, the fetch worker, the
	// two providers -- is real.
	filePath := path.Join(seriesLive.Status.Path, "Season 01", "Fixture.Subtitle.Episode.S01E01.CLUSTARR.mkv")
	plantMedia(t, hostPath(filePath))

	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-sub13-thr-mf"), Namespace: Namespace},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: episode.Name},
			Path:     filePath,
		},
	}
	require.NoError(t, k8s.SetControllerReference(&episode, mf, k8sClient.Scheme()))
	require.NoError(t, k8sClient.Create(ctx, mf))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), mf) })
	probed := waitForMediaFileProbed(ctx, t, client.ObjectKeyFromObject(mf))

	osProvider := &subtitlev1alpha1.SubtitleProvider{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-sub13-thr-os"), Namespace: Namespace},
		Spec: subtitlev1alpha1.SubtitleProviderSpec{
			Type: subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, Enabled: true, Priority: 10,
			SecretRef: &corev1.LocalObjectReference{Name: "opensubtitles-fixture-credentials"},
			Endpoint:  ptr.To("http://opensubtitles-stub." + Namespace + ".svc"),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, osProvider))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), osProvider) })

	gdProvider := &subtitlev1alpha1.SubtitleProvider{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-sub13-thr-gd"), Namespace: Namespace},
		Spec: subtitlev1alpha1.SubtitleProviderSpec{
			Type: subtitlev1alpha1.SubtitleProviderGestdown, Enabled: true, Priority: 20,
			Endpoint: ptr.To("http://gestdown-stub." + Namespace + ".svc"),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, gdProvider))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), gdProvider) })

	// Set the throttle mode BEFORE the profile (and so the request, and so
	// the first search) exists, so the very first task this test drives
	// already observes it -- no race with an "ok" search slipping through
	// first.
	setOpenSubtitlesMode(ctx, t, opensubtitlesstub.ModeThrottled)
	t.Cleanup(func() { setOpenSubtitlesMode(context.Background(), t, opensubtitlesstub.ModeOK) })

	profile := &subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-sub13-thr-profile")},
		Spec: subtitlev1alpha1.SubtitleProfileSpec{
			// kind=episode alone is a safe selector for this scenario, and
			// only this one: series root-folder scanning is unsupported
			// (this file's package doc comment), so no OTHER scenario in
			// this suite produces a real, probed Episode-kind MediaFile as
			// of this writing. A future scenario that does must give this
			// one a narrower selector the same way subtitleMovieFolder's
			// resolution/source pair does for the movie leg.
			Selector:        &metav1.LabelSelector{MatchLabels: map[string]string{catalogv1alpha1.LabelKind: string(commonv1.MediaKindEpisode)}},
			Languages:       []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}},
			MinScorePercent: subtitlev1alpha1.ScorePct{Movie: 10, Episode: 10},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, profile))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), profile) })
	waitForSubtitleProfileReady(ctx, t, profile.Name)

	reqKey := client.ObjectKey{Namespace: Namespace, Name: probed.Name}
	cleanupUnlessFailed(t, func() {
		_ = k8sClient.Delete(context.Background(), &subtitlev1alpha1.SubtitleRequest{
			ObjectMeta: metav1.ObjectMeta{Name: probed.Name, Namespace: Namespace},
		})
	})

	waitFor(t, ctx, subtitleFetchTimeout, "SubtitleRequest "+probed.Name+" en downloaded from gestdown after opensubtitles throttled",
		func(ctx context.Context) (bool, error) {
			var live subtitlev1alpha1.SubtitleRequest
			if err := k8sClient.Get(ctx, reqKey, &live); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			en := subtitleItem(live.Status.Items, "en")
			return itemDownloaded(en) && en.Provider == gdProvider.Name, nil
		}, describeSubtitleRequest(reqKey))

	// The shared throttle actually benched the OpenSubtitles.com provider --
	// ruling R2's projection, not merely "the search happened to prefer
	// gestdown".
	waitFor(t, ctx, 2*time.Minute, "SubtitleProvider "+osProvider.Name+" Throttled=True",
		func(ctx context.Context) (bool, error) {
			var live subtitlev1alpha1.SubtitleProvider
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(osProvider), &live); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			return isConditionTrue(live.Status.Conditions, subtitlev1alpha1.SubtitleProviderConditionThrottled) &&
				live.Status.ThrottledUntil != nil && live.Status.ThrottledUntil.After(time.Now()), nil
		}, describeSubtitleProvider(client.ObjectKeyFromObject(osProvider)))

	// Direct HTTP evidence, independent of how the CRDs are interpreted:
	// the mock actually answered 429 and gestdown actually answered 200.
	osEntries := readOpenSubtitlesRequestsSince(t, t0)
	require.NotEmpty(t, osEntries, "opensubtitles-stub logged no request since this scenario started")
	var sawThrottled bool
	for _, e := range osEntries {
		if e.Path == "/subtitles" && e.Status == http.StatusTooManyRequests {
			sawThrottled = true
		}
	}
	assert.True(t, sawThrottled, "expected opensubtitles-stub to have answered /subtitles 429 at least once; entries=%+v", osEntries)

	gdEntries := readGestdownRequestsSince(t, t0)
	require.NotEmpty(t, gdEntries, "gestdown-stub logged no request since this scenario started")
	var sawGestdownSearch bool
	for _, e := range gdEntries {
		if strings.HasPrefix(e.Path, "/subtitles/get/") && e.Status == http.StatusOK {
			sawGestdownSearch = true
		}
	}
	assert.True(t, sawGestdownSearch, "expected gestdown-stub's subtitle search to have been reached after the fallthrough; entries=%+v", gdEntries)
}

// describeSubtitleProvider renders one SubtitleProvider's throttle state and
// conditions for a failure message.
func describeSubtitleProvider(key client.ObjectKey) func() string {
	return func() string {
		var live subtitlev1alpha1.SubtitleProvider
		if err := k8sClient.Get(context.Background(), key, &live); err != nil {
			return fmt.Sprintf("SubtitleProvider %s could not be read back: %v", key.Name, err)
		}
		out := fmt.Sprintf("SubtitleProvider %s throttledUntil=%v throttleReason=%q errorsLast120s=%d",
			key.Name, live.Status.ThrottledUntil, live.Status.ThrottleReason, live.Status.ErrorsLast120s)
		for _, c := range live.Status.Conditions {
			out += fmt.Sprintf("\n    condition %s=%s reason=%s message=%q", c.Type, c.Status, c.Reason, c.Message)
		}
		return out
	}
}

// ---------------------------------------------------------------------------
// Scenario 1's subtitle leg: permanently blocked, same reason as its
// transcode leg.
// ---------------------------------------------------------------------------

// TestDownloadScenario1SubtitleLegBlocked documents why scenario 1's
// subtitle leg ("MediaFile -> SubtitleRequest satisfied") cannot be added
// to download_test.go's TestDownloadTorrentGrabToImportAttempt, mirroring
// transcode_test.go's TestDownloadScenario1TranscodeLegBlocked exactly:
// the download-import route never reaches a MediaFile at all
// (helpers_test.go's importGapReason -- test/fixtures/seeder and
// test/fixtures/nntpstub both always name their downloaded content
// "clustarr-fixture.bin", an extension outside pkg/fsops.MediaExtensions),
// so captionarr's SubtitleProfile controller -- which watches MediaFile,
// not Download -- has structurally nothing to attach a SubtitleRequest to.
//
// Checked against pkg/fsops/classify.go at HEAD (2026-09-23), not assumed:
// MediaExtensions is still exactly the set importGapReason names. A
// concurrent task may be changing it (this task's brief warned as much);
// if it has landed, this skip is stale and should be replaced with a real
// extension of TestDownloadTorrentGrabToImportAttempt through a
// SubtitleRequest, the same way this file's main scenario extends a real
// MediaFile.
//
// TestSubtitleRequestSidecarPipelineAndLanguageRemoval (this file) is how
// scenario 13's own MediaFile leg IS reached -- through the LibraryScan
// route to a real MediaFile, not a download import.
func TestDownloadScenario1SubtitleLegBlocked(t *testing.T) {
	t.Skip("scenario 1's subtitle leg (\"MediaFile -> SubtitleRequest satisfied\") can never run " +
		"against test/fixtures/seeder or test/fixtures/nntpstub as they exist today: " + importGapReason)
}
