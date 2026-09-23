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

package subtitlerequest_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/controller/subtitlerequest"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

const day = 24 * time.Hour

// movieThreshold is the CRD-default minScorePercent.movie (70) as an absolute
// score -- what every first-download fetch task for a movie carries.
var movieThreshold = int32(subtitles.MinScore(commonv1alpha1.MediaKindMovie, 70))

var (
	controllerTop = []string{
		"observedGeneration", "phase", "profileGeneration", "probeHash",
		"fileFingerprint", "existing", "conditions", "items",
	}
	controllerItem = []string{"langKey", "attempts", "nextSearchAt"}
	workerLeaves   = []string{"langKey", "state", "score", "scoreOutOf", "provider", "subtitleID", "path", "lastError"}
)

func withDownloadedAt(l []string) []string {
	return append(append([]string(nil), l...), "downloadedAt")
}

// TestFirstPlanCreatesTheItemAndPublishesOnlyWhatNothingCovers: the English
// track is tagged "eng" by ffprobe and French is a sidecar named with its
// ISO 639-2 code, so German is the only wanted language. The controller
// creates its item -- langKey, nextSearchAt and attempts, and NO state, which
// is what "planned, never searched" looks like -- and publishes one task.
func TestFirstPlanCreatesTheItemAndPublishesOnlyWhatNothingCovers(t *testing.T) {
	f := newFixture(t, "sr-first")
	p := f.profile(func(s *subtitlev1alpha1.SubtitleProfileSpec) {
		s.Languages = []subtitlev1alpha1.LanguageItem{lang("en", "en"), lang("fr", "fr"), lang("de", "de")}
	})
	f.mediaFile("movie", englishTrack(), "movie.fre.srt", "movie.nfo")
	sr := f.request("movie")

	res := f.reconcile("movie")
	got := f.get("movie")

	assert.Equal(t, subtitlev1alpha1.SubtitleRequestPhaseSearching, got.Status.Phase)
	require.Len(t, got.Status.Existing, 2)
	assert.Equal(t, subtitlev1alpha1.ExistingSub{
		LangKey: "en", Source: subtitlev1alpha1.SubtitleSourceEmbedded, Path: "movie.mkv", StreamIndex: ptrTo[int32](2),
	}, got.Status.Existing[0])
	assert.Equal(t, subtitlev1alpha1.ExistingSub{
		LangKey: "fr", Source: subtitlev1alpha1.SubtitleSourceSidecar, Path: "movie.fre.srt",
	}, got.Status.Existing[1])
	assert.Equal(t, probeHash1, got.Status.ProbeHash)
	assert.Equal(t, p.Generation, got.Status.ProfileGeneration)
	assert.Equal(t, sr.Generation, got.Status.ObservedGeneration)
	if assert.NotNil(t, got.Status.FileFingerprint) {
		assert.EqualValues(t, 4<<30, got.Status.FileFingerprint.SizeBytes)
		assert.Equal(t, testStart.Add(-48*time.Hour), timeOf(t, got.Status.FileFingerprint.ModTime))
	}

	require.Len(t, got.Status.Items, 1, "one item, for the one wanted language")
	de := item(t, got, "de")
	assert.Empty(t, de.State, "a created-but-never-searched item carries no state")
	assert.EqualValues(t, 1, de.Attempts.Count)
	assert.Equal(t, testStart, timeOf(t, de.Attempts.Initial))
	assert.Equal(t, testStart.Add(6*time.Hour), timeOf(t, de.NextSearchAt))
	assertManagedFieldsSplit(t, got,
		map[k8s.FieldManager][]string{k8s.ManagerCaptionarr: controllerTop},
		map[k8s.FieldManager]map[string][]string{k8s.ManagerCaptionarr: {"de": controllerItem}})

	assert.Equal(t, metav1.ConditionTrue, cond(got, subtitlev1alpha1.SubtitleRequestConditionPlanned).Status)
	sat := cond(got, subtitlev1alpha1.SubtitleRequestConditionSatisfied)
	assert.Equal(t, metav1.ConditionFalse, sat.Status)
	assert.Equal(t, "missing: de", sat.Message)
	assert.Equal(t, metav1.ConditionFalse, cond(got, subtitlev1alpha1.SubtitleRequestConditionCutoffMet).Status)

	calls := f.bus.calls()
	require.Len(t, calls, 1, "only German is missing")
	c := calls[0]
	assert.Equal(t, events.WorkFetchSubject(events.PriorityNormal, string(got.UID), "de"), c.subject)
	assert.Equal(t, events.MsgIDForSubtitle(string(got.UID), "de", probeHash1), c.msgID, "R6")
	assert.Equal(t, "de", c.task.LangKey)
	assert.Equal(t, movieThreshold, c.task.MinScore)
	assert.Equal(t, probeHash1, c.task.ProbeHash)
	assert.False(t, c.task.Upgrade)
	assert.Equal(t, f.ns, c.task.RequestRef.Namespace)
	assert.Equal(t, "movie", c.task.RequestRef.Name)
	assert.Equal(t, string(got.UID), c.task.RequestRef.UID)
	assert.Equal(t, subtitlerequest.FetchTaskType, c.env.Type)
	assert.Equal(t, f.ns+"/movie", c.env.Key)
	assert.Equal(t, 6*time.Hour, res.RequeueAfter)

	// Nothing is due again until nextSearchAt, and the unreported item keeps
	// the request Searching.
	f.reconcile("movie")
	assert.Len(t, f.bus.calls(), 1)
	assert.Equal(t, subtitlev1alpha1.SubtitleRequestPhaseSearching, f.get("movie").Status.Phase)
}

// TestR6DedupAbsorbsTheRepublishAfterAFailedApply is R6's reason to exist: a
// task goes out, the status apply that would record it fails, and the
// level-driven retry publishes again. The deterministic message ID makes the
// stream absorb the repeat, and the retry records the dispatch it absorbed.
func TestR6DedupAbsorbsTheRepublishAfterAFailedApply(t *testing.T) {
	f := newFixture(t, "sr-r6")
	f.profile(nil)
	f.mediaFile("movie", englishTrack())
	f.request("movie")

	wc, err := client.NewWithWatch(testCfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	failed := false
	flaky := interceptor.NewClient(wc, interceptor.Funcs{
		SubResourceApply: func(ctx context.Context, c client.Client, sub string, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
			if !failed {
				failed = true
				return errors.New("injected: the apiserver went away")
			}
			return c.SubResource(sub).Apply(ctx, obj, opts...)
		},
	})
	r := &subtitlerequest.Reconciler{Client: flaky, Bus: f.bus, Now: f.clock.Now}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: f.ns, Name: "movie"}}

	_, err = r.Reconcile(f.ctx, req)
	require.Error(t, err, "the injected apply failure must surface")
	require.Len(t, f.bus.stored(), 1, "the task went out before the apply failed")
	assert.Empty(t, f.get("movie").Status.Items, "and nothing recorded it")

	f.clock.Advance(time.Minute)
	_, err = r.Reconcile(f.ctx, req)
	require.NoError(t, err)
	calls := f.bus.calls()
	require.Len(t, calls, 2)
	assert.True(t, calls[1].dup, "the retry's publish must be absorbed by the dedup window")
	assert.Len(t, f.bus.stored(), 1, "one task, not two")
	de := item(t, f.get("movie"), "de")
	assert.EqualValues(t, 1, de.Attempts.Count, "the absorbed dispatch is recorded exactly once")
}

// TestAdaptiveGateBacksOff walks one language through the worker's first
// report, the full 6h cadence, and the adaptive back-off after three weeks,
// asserting the RequeueAfter each step computes: none of these wake-ups is a
// watch event.
func TestAdaptiveGateBacksOff(t *testing.T) {
	f := newFixture(t, "sr-adaptive")
	f.profile(nil)
	f.mediaFile("movie", englishTrack())
	f.request("movie")
	f.reconcile("movie")

	f.report("movie", "de", func(it *subtitlev1alpha1.SubtitleItem) {
		it.State = subtitlev1alpha1.SubtitleItemUnavailable
		it.ScoreOutOf = 180
		it.LastError = "no candidate"
	})
	res := f.reconcile("movie")
	got := f.get("movie")
	de := item(t, got, "de")
	assert.EqualValues(t, 1, de.Attempts.Count)
	assert.Equal(t, testStart.Add(6*time.Hour), timeOf(t, de.NextSearchAt))
	assert.Equal(t, 6*time.Hour, res.RequeueAfter)
	assert.Equal(t, subtitlev1alpha1.SubtitleRequestPhaseWanted, got.Status.Phase)
	assert.Equal(t, subtitlev1alpha1.SubtitleItemUnavailable, de.State, "the worker's state survives the controller's apply")
	assert.Equal(t, "no candidate", de.LastError)

	// Nothing is due before nextSearchAt.
	f.clock.Advance(5 * time.Hour)
	res = f.reconcile("movie")
	assert.Len(t, f.bus.calls(), 1)
	assert.Equal(t, time.Hour, res.RequeueAfter)

	// Full cadence: due at +6h, outside the dedup window, so a new task.
	f.clock.Advance(time.Hour)
	res = f.reconcile("movie")
	require.Len(t, f.bus.stored(), 2)
	de = item(t, f.get("movie"), "de")
	assert.EqualValues(t, 2, de.Attempts.Count)
	assert.Equal(t, testStart, timeOf(t, de.Attempts.Initial), "initial never moves")
	assert.Equal(t, testStart.Add(12*time.Hour), timeOf(t, de.NextSearchAt))
	assert.Equal(t, 6*time.Hour, res.RequeueAfter)
	assert.Equal(t, subtitlev1alpha1.SubtitleRequestPhaseSearching, f.get("movie").Status.Phase)

	// Past initial+3w: this search still goes (it was due inside the
	// window), but the next one is latest+1w, not latest+6h.
	f.clock.Advance(22*day - 6*time.Hour)
	res = f.reconcile("movie")
	require.Len(t, f.bus.stored(), 3)
	de = item(t, f.get("movie"), "de")
	assert.EqualValues(t, 3, de.Attempts.Count)
	assert.Equal(t, testStart.Add(29*day), timeOf(t, de.NextSearchAt), "adaptive: latest+1w once initial+3w has passed")
	assert.Equal(t, 7*day, res.RequeueAfter)

	f.clock.Advance(day)
	res = f.reconcile("movie")
	assert.Len(t, f.bus.stored(), 3, "the gate is closed until latest+1w")
	assert.Equal(t, 6*day, res.RequeueAfter)
	assert.Equal(t, subtitlev1alpha1.SubtitleRequestPhaseWanted, f.get("movie").Status.Phase)
}

// TestUpgradePass reads the worker's score, scoreOutOf and downloadedAt --
// never writing them -- and publishes a low-priority upgrade task with
// minScore = score+1 for the item still < outOf−3, every 12h, inside the
// lookback window.
func TestUpgradePass(t *testing.T) {
	f := newFixture(t, "sr-upgrade")
	f.profile(func(s *subtitlev1alpha1.SubtitleProfileSpec) {
		s.Languages = []subtitlev1alpha1.LanguageItem{lang("en", "en"), lang("fr", "fr")}
		s.Cutoff = nil
	})
	f.mediaFile("movie", &commonv1alpha1.MediaInfo{Audio: []commonv1alpha1.AudioStream{{Language: "eng"}}})
	f.request("movie")
	f.reconcile("movie")
	require.Len(t, f.bus.stored(), 2, "setup: en and fr are wanted")

	// The worker downloads both: sidecar first, then the report.
	now := metav1.NewTime(f.clock.Now())
	f.sidecar("movie.en.srt")
	f.report("movie", "en", func(it *subtitlev1alpha1.SubtitleItem) {
		it.State, it.Score, it.ScoreOutOf = subtitlev1alpha1.SubtitleItemUpgradable, 150, 180
		it.Provider, it.SubtitleID, it.Path, it.DownloadedAt = "gestdown", "g-1", "movie.en.srt", &now
	})
	f.sidecar("movie.fr.srt")
	f.report("movie", "fr", func(it *subtitlev1alpha1.SubtitleItem) {
		it.State, it.Score, it.ScoreOutOf = subtitlev1alpha1.SubtitleItemDownloaded, 178, 180
		it.Provider, it.SubtitleID, it.Path, it.DownloadedAt = "gestdown", "g-2", "movie.fr.srt", &now
	})

	res := f.reconcile("movie")
	got := f.get("movie")
	assert.Equal(t, subtitlev1alpha1.SubtitleRequestPhaseSatisfied, got.Status.Phase)
	assert.Len(t, got.Status.Existing, 2, "the sidecars the worker wrote count as existing")
	assert.Equal(t, 12*time.Hour, res.RequeueAfter, "the upgrade pass is a computed wake-up")
	assert.Equal(t, testStart.Add(12*time.Hour), timeOf(t, item(t, got, "en").NextSearchAt))
	assert.NotNil(t, item(t, got, "fr").NextSearchAt, "a satisfied item stays live: it carries its next upgrade check")
	assert.Len(t, f.bus.calls(), 2)

	f.clock.Advance(12 * time.Hour)
	res = f.reconcile("movie")
	got = f.get("movie")
	calls := f.bus.calls()
	require.Len(t, calls, 3, "fr at 178/180 is not < outOf−3")
	c := calls[2]
	assert.Equal(t, events.WorkFetchSubject(events.PriorityLow, string(got.UID), "en"), c.subject)
	assert.True(t, c.task.Upgrade)
	assert.EqualValues(t, 151, c.task.MinScore, "§6.5: minScore = score+1")
	en := item(t, got, "en")
	assert.EqualValues(t, 2, en.Attempts.Count)
	assert.Equal(t, testStart.Add(24*time.Hour), timeOf(t, en.NextSearchAt))
	assert.EqualValues(t, 150, en.Score, "the worker's score is read, not written")
	assert.Equal(t, 12*time.Hour, res.RequeueAfter)
	assert.Equal(t, subtitlev1alpha1.SubtitleRequestPhaseSearching, got.Status.Phase)

	// Past the seven-day lookback the item is never upgraded again, and no
	// wake-up is scheduled for it -- but it stays live.
	f.clock.Advance(7 * day)
	res = f.reconcile("movie")
	assert.Len(t, f.bus.calls(), 3)
	assert.NotNil(t, item(t, f.get("movie"), "en").NextSearchAt)
	assert.Zero(t, res.RequeueAfter)
}

// steadyState drives a request to a populated state under BOTH managers: the
// controller's plan, its created "de" item and schedule, and the worker's
// report on it.
func steadyState(t *testing.T, f *fixture) *subtitlev1alpha1.SubtitleRequest {
	t.Helper()
	f.profile(nil)
	f.mediaFile("movie", englishTrack())
	f.request("movie")
	f.reconcile("movie")
	f.report("movie", "de", func(it *subtitlev1alpha1.SubtitleItem) {
		it.State = subtitlev1alpha1.SubtitleItemUnavailable
		it.Score, it.ScoreOutOf = 12, 180
		it.Provider, it.SubtitleID = "gestdown", "none"
		it.LastError = "no candidate above 126"
	})
	f.reconcile("movie")
	sr := f.get("movie")
	require.NotNil(t, item(t, sr, "de").NextSearchAt, "setup: the controller did not schedule de")
	require.Len(t, sr.Status.Existing, 1, "setup: the eng track did not count")
	return sr
}

// TestBlockedPathsRedeclareTheSteadyState acts on an object that already
// carries both managers' status, and blocks it two different ways. A
// Blocked apply that dropped any owned field would release it -- the
// early-return bug CLAUDE.md records three times -- so every controller
// field and every worker leaf must survive, and managedFields must still
// show the exact R4 split.
func TestBlockedPathsRedeclareTheSteadyState(t *testing.T) {
	f := newFixture(t, "sr-blocked")
	before := steadyState(t, f)

	split := func(sr *subtitlev1alpha1.SubtitleRequest) {
		t.Helper()
		assertManagedFieldsSplit(t, sr,
			map[k8s.FieldManager][]string{k8s.ManagerCaptionarr: controllerTop, k8s.ManagerCaptionarrWorker: {"items"}},
			map[k8s.FieldManager]map[string][]string{
				k8s.ManagerCaptionarr:       {"de": controllerItem},
				k8s.ManagerCaptionarrWorker: {"de": workerLeaves},
			})
	}
	split(before)

	survived := func(after *subtitlev1alpha1.SubtitleRequest, who string) {
		t.Helper()
		assert.Equal(t, subtitlev1alpha1.SubtitleRequestPhaseBlocked, after.Status.Phase, who)
		assert.Equal(t, before.Status.Existing, after.Status.Existing, who+" released existing")
		assert.Equal(t, before.Status.ProbeHash, after.Status.ProbeHash, who+" released probeHash")
		assert.Equal(t, before.Status.ProfileGeneration, after.Status.ProfileGeneration, who+" released profileGeneration")
		assert.Equal(t, before.Status.FileFingerprint, after.Status.FileFingerprint, who+" released fileFingerprint")
		assert.Equal(t, before.Status.ObservedGeneration, after.Status.ObservedGeneration, who)
		b, a := item(t, before, "de"), item(t, after, "de")
		assert.Equal(t, b.Attempts, a.Attempts, who+" released attempts")
		assert.Equal(t, b.NextSearchAt, a.NextSearchAt, who+" released nextSearchAt")
		assert.Equal(t, b, a, who+" changed a worker leaf")
		for _, ct := range []string{subtitlev1alpha1.SubtitleRequestConditionSatisfied, subtitlev1alpha1.SubtitleRequestConditionCutoffMet} {
			assert.Equal(t, cond(before, ct).Status, cond(after, ct).Status, who+" released "+ct)
			assert.Equal(t, cond(before, ct).Reason, cond(after, ct).Reason, who+" released "+ct)
		}
		split(after)
	}

	// Block one: the video vanished from its directory.
	require.NoError(t, os.Remove(filepath.Join(f.dir, "movie.mkv")))
	res := f.reconcile("movie")
	after := f.get("movie")
	survived(after, "the not-on-disk block")
	planned := cond(after, subtitlev1alpha1.SubtitleRequestConditionPlanned)
	assert.Equal(t, metav1.ConditionFalse, planned.Status)
	assert.Equal(t, subtitlerequest.ReasonNotOnDisk, planned.Reason)
	assert.Equal(t, 5*time.Minute, res.RequeueAfter, "no watch clears a disk-level block")
	assert.Len(t, f.bus.calls(), 1, "a blocked request publishes nothing")

	// Block two: the MediaFile is gone.
	require.NoError(t, f.c.Delete(f.ctx, &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "movie", Namespace: f.ns}}))
	f.reconcile("movie")
	after = f.get("movie")
	survived(after, "the MediaFile-not-found block")
	assert.Equal(t, subtitlerequest.ReasonMediaFileNotFound, cond(after, subtitlev1alpha1.SubtitleRequestConditionPlanned).Reason)
}

// TestHappyPathOverAnObjectWithBothManagers: the controller's normal apply,
// made on top of the worker's reports, keeps every worker leaf and owns only
// langKey, attempts and nextSearchAt per item -- on a missing language and
// on a satisfied one.
func TestHappyPathOverAnObjectWithBothManagers(t *testing.T) {
	f := newFixture(t, "sr-happy")
	f.profile(nil)
	f.mediaFile("movie", &commonv1alpha1.MediaInfo{})
	f.request("movie")
	f.reconcile("movie")
	dl := metav1.NewTime(testStart)
	f.report("movie", "de", func(it *subtitlev1alpha1.SubtitleItem) {
		it.State, it.LastError = subtitlev1alpha1.SubtitleItemFailed, "timeout"
	})
	f.sidecar("movie.en.srt")
	f.report("movie", "en", func(it *subtitlev1alpha1.SubtitleItem) {
		it.State, it.Score, it.ScoreOutOf = subtitlev1alpha1.SubtitleItemDownloaded, 179, 180
		it.Provider, it.SubtitleID, it.Path, it.DownloadedAt = "gestdown", "x", "movie.en.srt", &dl
	})
	f.reconcile("movie")
	got := f.get("movie")

	en := item(t, got, "en")
	assert.Equal(t, subtitlev1alpha1.SubtitleItemDownloaded, en.State)
	assert.EqualValues(t, 179, en.Score)
	assert.Equal(t, "movie.en.srt", en.Path)
	assert.NotNil(t, en.DownloadedAt)
	assert.NotNil(t, en.NextSearchAt, "the satisfied item stays live")
	assert.Equal(t, "timeout", item(t, got, "de").LastError)

	assertManagedFieldsSplit(t, got,
		map[k8s.FieldManager][]string{k8s.ManagerCaptionarr: controllerTop, k8s.ManagerCaptionarrWorker: {"items"}},
		map[k8s.FieldManager]map[string][]string{
			k8s.ManagerCaptionarr:       {"de": controllerItem, "en": controllerItem},
			k8s.ManagerCaptionarrWorker: {"de": workerLeaves, "en": withDownloadedAt(workerLeaves)},
		})
}

// TestDroppedLanguageIsRemovedFromItems is the removal half of the liveness
// protocol. An item BOTH managers have written must leave status.items once
// its language leaves the profile: the controller stops sending it, the
// worker -- even one that races the drop from a stale read -- stops
// re-sending it on its next fresh apply, nothing owns the entry, and
// server-side apply deletes it. It must then stay gone.
func TestDroppedLanguageIsRemovedFromItems(t *testing.T) {
	f := newFixture(t, "sr-drop")
	p := f.profile(nil)
	f.mediaFile("movie", &commonv1alpha1.MediaInfo{})
	f.request("movie")
	f.reconcile("movie")
	dl := metav1.NewTime(testStart)
	f.sidecar("movie.en.srt")
	f.report("movie", "en", func(it *subtitlev1alpha1.SubtitleItem) {
		it.State, it.Score, it.ScoreOutOf = subtitlev1alpha1.SubtitleItemDownloaded, 150, 180
		it.Provider, it.SubtitleID, it.Path, it.DownloadedAt = "gestdown", "g-1", "movie.en.srt", &dl
	})
	f.report("movie", "de", func(it *subtitlev1alpha1.SubtitleItem) { it.State = subtitlev1alpha1.SubtitleItemUnavailable })
	f.reconcile("movie")
	both := f.get("movie")
	assertManagedFieldsSplit(t, both,
		map[k8s.FieldManager][]string{k8s.ManagerCaptionarr: controllerTop, k8s.ManagerCaptionarrWorker: {"items"}},
		map[k8s.FieldManager]map[string][]string{
			k8s.ManagerCaptionarr:       {"de": controllerItem, "en": controllerItem},
			k8s.ManagerCaptionarrWorker: {"de": workerLeaves, "en": withDownloadedAt(workerLeaves)},
		})
	stale := both.DeepCopy() // a worker read taken before the drop

	patch := client.MergeFrom(p.DeepCopy())
	p.Spec.Languages = []subtitlev1alpha1.LanguageItem{lang("de", "de")}
	require.NoError(t, f.c.Patch(f.ctx, p, patch))
	f.reconcile("movie")

	afterDrop := f.get("movie")
	en := item(t, afterDrop, "en")
	assert.Nil(t, en.NextSearchAt, "the controller released en's nextSearchAt: en is no longer live")
	assert.Zero(t, en.Attempts, "and its attempts")
	assert.Equal(t, subtitlev1alpha1.SubtitleItemDownloaded, en.State, "the worker has not applied yet, so its leaves remain")
	// The en sidecar is no longer in a profile language, so existing is
	// empty and, correctly, no longer declared at all.
	assert.Empty(t, afterDrop.Status.Existing)
	noExisting := slices.DeleteFunc(slices.Clone(controllerTop), func(s string) bool { return s == "existing" })
	assertManagedFieldsSplit(t, afterDrop,
		map[k8s.FieldManager][]string{k8s.ManagerCaptionarr: noExisting, k8s.ManagerCaptionarrWorker: {"items"}},
		map[k8s.FieldManager]map[string][]string{
			k8s.ManagerCaptionarr:       {"de": controllerItem},
			k8s.ManagerCaptionarrWorker: {"de": workerLeaves, "en": withDownloadedAt(workerLeaves)},
		})

	// The worker races the drop: it applies from the read it took before.
	f.workerApplyFrom(stale)
	assert.Len(t, f.get("movie").Status.Items, 2, "a racing worker re-sends en once")

	// Its next apply reads fresh, sees en is not live, and drops it: nothing
	// owns the entry any more, so server-side apply deletes it.
	f.workerApply("movie")
	gone := f.get("movie")
	require.Len(t, gone.Status.Items, 1)
	assert.Equal(t, "de", gone.Status.Items[0].LangKey)
	assert.Equal(t, subtitlev1alpha1.SubtitleItemUnavailable, gone.Status.Items[0].State, "de is untouched")
	assertManagedFieldsSplit(t, gone,
		map[k8s.FieldManager][]string{k8s.ManagerCaptionarr: noExisting, k8s.ManagerCaptionarrWorker: {"items"}},
		map[k8s.FieldManager]map[string][]string{
			k8s.ManagerCaptionarr:       {"de": controllerItem},
			k8s.ManagerCaptionarrWorker: {"de": workerLeaves},
		})

	// And it stays gone: one more worker apply, one more reconcile.
	f.workerApply("movie")
	f.reconcile("movie")
	final := f.get("movie")
	require.Len(t, final.Status.Items, 1)
	assert.Equal(t, "de", final.Status.Items[0].LangKey)
}

// TestBlockedApplyDoesNotReclaimADroppedItem: a request that blocks between
// the controller's drop and the worker's next apply must not re-declare the
// dropped item. Sending even its langKey would make the controller a co-owner
// again, and the entry would outlive the worker's release forever.
func TestBlockedApplyDoesNotReclaimADroppedItem(t *testing.T) {
	f := newFixture(t, "sr-drop-blocked")
	p := f.profile(nil)
	f.mediaFile("movie", &commonv1alpha1.MediaInfo{})
	f.request("movie")
	f.reconcile("movie")
	f.report("movie", "en", func(it *subtitlev1alpha1.SubtitleItem) { it.State = subtitlev1alpha1.SubtitleItemUnavailable })

	patch := client.MergeFrom(p.DeepCopy())
	p.Spec.Languages = []subtitlev1alpha1.LanguageItem{lang("de", "de")}
	require.NoError(t, f.c.Patch(f.ctx, p, patch))
	f.reconcile("movie")
	require.Nil(t, item(t, f.get("movie"), "en").NextSearchAt, "setup: en was not dropped")

	require.NoError(t, os.Remove(filepath.Join(f.dir, "movie.mkv")))
	f.reconcile("movie")
	blocked := f.get("movie")
	require.Equal(t, subtitlev1alpha1.SubtitleRequestPhaseBlocked, blocked.Status.Phase)

	f.workerApply("movie")
	got := f.get("movie")
	require.Len(t, got.Status.Items, 1, "the Blocked apply must not have re-claimed en")
	assert.Equal(t, "de", got.Status.Items[0].LangKey)
}

// TestNewProbeHashResetsTheSchedule: a replaced file is searched now, not on
// the old file's schedule, under a message ID the old task cannot dedup.
func TestNewProbeHashResetsTheSchedule(t *testing.T) {
	f := newFixture(t, "sr-reprobe")
	steadyState(t, f)
	require.Len(t, f.bus.stored(), 1)

	f.clock.Advance(30 * time.Minute)
	f.probe("movie", "hash-2", *englishTrack())
	res := f.reconcile("movie")
	got := f.get("movie")

	assert.Equal(t, "hash-2", got.Status.ProbeHash)
	stored := f.bus.stored()
	require.Len(t, stored, 2, "the new file's task must not be absorbed by the old file's")
	assert.Equal(t, events.MsgIDForSubtitle(string(got.UID), "de", "hash-2"), stored[1].msgID)
	de := item(t, got, "de")
	assert.EqualValues(t, 1, de.Attempts.Count, "the old file's history is gone")
	assert.Equal(t, testStart.Add(30*time.Minute), timeOf(t, de.Attempts.Initial))
	assert.Equal(t, 6*time.Hour, res.RequeueAfter)
}

// TestForceSearch: spec.forceSearch sends every unsatisfied language now, at
// high priority, and is reset. Inside the dedup window the forced task is
// absorbed and the attempt is not counted twice.
func TestForceSearch(t *testing.T) {
	f := newFixture(t, "sr-force")
	steadyState(t, f)

	force := func() {
		t.Helper()
		sr := f.get("movie")
		patch := client.MergeFrom(sr.DeepCopy())
		sr.Spec.ForceSearch = true
		require.NoError(t, f.c.Patch(f.ctx, sr, patch))
	}

	f.clock.Advance(2 * time.Hour) // de is not due until +6h
	force()
	f.reconcile("movie")
	got := f.get("movie")
	stored := f.bus.stored()
	require.Len(t, stored, 2)
	assert.Equal(t, events.WorkFetchSubject(events.PriorityHigh, string(got.UID), "de"), stored[1].subject)
	assert.False(t, got.Spec.ForceSearch, "forceSearch is one-shot")
	de := item(t, got, "de")
	assert.EqualValues(t, 2, de.Attempts.Count)
	assert.Equal(t, testStart.Add(8*time.Hour), timeOf(t, de.NextSearchAt))

	f.clock.Advance(10 * time.Minute)
	force()
	f.reconcile("movie")
	got = f.get("movie")
	calls := f.bus.calls()
	assert.True(t, calls[len(calls)-1].dup, "a forced search inside the window is absorbed")
	assert.EqualValues(t, 2, item(t, got, "de").Attempts.Count, "and is not counted twice")
	assert.False(t, got.Spec.ForceSearch)

	// The reset is an Update-operation patch in its own managedFields
	// entry, so it can never release the profile controller's apply-owned
	// spec fields, which share the captionarr manager name.
	for _, e := range got.ManagedFields {
		if e.Manager == k8s.ManagerCaptionarr.String() && e.Subresource == "" {
			assert.Equal(t, metav1.ManagedFieldsOperationUpdate, e.Operation)
		}
	}
}

func TestBlocksBeforePlanning(t *testing.T) {
	f := newFixture(t, "sr-preplan")
	f.profile(nil)

	f.request("ghost")
	f.reconcile("ghost")
	got := f.get("ghost")
	assert.Equal(t, subtitlev1alpha1.SubtitleRequestPhaseBlocked, got.Status.Phase)
	assert.Equal(t, subtitlerequest.ReasonMediaFileNotFound, cond(got, subtitlev1alpha1.SubtitleRequestConditionPlanned).Reason)
	assert.Equal(t, metav1.ConditionUnknown, cond(got, subtitlev1alpha1.SubtitleRequestConditionSatisfied).Status)
	assert.Equal(t, metav1.ConditionUnknown, cond(got, subtitlev1alpha1.SubtitleRequestConditionCutoffMet).Status)

	f.mediaFile("unprobed", nil)
	f.request("unprobed")
	res := f.reconcile("unprobed")
	assert.Equal(t, subtitlerequest.ReasonAwaitingProbe, cond(f.get("unprobed"), subtitlev1alpha1.SubtitleRequestConditionPlanned).Reason)
	assert.Zero(t, res.RequeueAfter, "the MediaFile's probeHash watch brings it back")

	f.mediaFile("orphan", englishTrack())
	sr := &subtitlev1alpha1.SubtitleRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: f.ns},
		Spec:       subtitlev1alpha1.SubtitleRequestSpec{MediaFileRef: "orphan", ProfileRef: "no-such-profile"},
	}
	require.NoError(t, f.c.Create(f.ctx, sr))
	f.reconcile("orphan")
	assert.Equal(t, subtitlerequest.ReasonProfileNotFound, cond(f.get("orphan"), subtitlev1alpha1.SubtitleRequestConditionPlanned).Reason)

	assert.Empty(t, f.bus.calls())
}

// TestProfileEditReplans: turning on ignorePGS makes an English PGS track
// stop counting, and the next reconcile plans against the new generation.
func TestProfileEditReplans(t *testing.T) {
	f := newFixture(t, "sr-profile")
	p := f.profile(nil)
	pgs := &commonv1alpha1.MediaInfo{Subtitles: []commonv1alpha1.SubtitleStream{
		{Index: 3, Codec: "hdmv_pgs_subtitle", Language: "eng", Bitmap: true},
	}}
	f.mediaFile("movie", pgs)
	f.request("movie")
	f.reconcile("movie")
	require.Len(t, f.get("movie").Status.Existing, 1, "a PGS track counts while ignorePGS is off")

	patch := client.MergeFrom(p.DeepCopy())
	p.Spec.Embedded.IgnorePGS = true
	require.NoError(t, f.c.Patch(f.ctx, p, patch))
	require.Greater(t, p.Generation, int64(1))

	f.reconcile("movie")
	got := f.get("movie")
	assert.Equal(t, p.Generation, got.Status.ProfileGeneration)
	assert.Empty(t, got.Status.Existing)
	var langs []string
	for _, c := range f.bus.stored() {
		langs = append(langs, c.task.LangKey)
	}
	assert.ElementsMatch(t, []string{"de", "en"}, langs)
	assert.Len(t, got.Status.Items, 2)
}

func ptrTo[T any](v T) *T { return &v }

// timeOf fails the test, rather than panicking the whole binary, when a
// timestamp the test expects is missing -- a panic would hide every later
// test's result from a falsification run.
func timeOf(t *testing.T, mt *metav1.Time) time.Time {
	t.Helper()
	require.NotNil(t, mt, "an expected timestamp is missing")
	return mt.UTC()
}
