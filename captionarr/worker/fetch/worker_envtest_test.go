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

package fetch_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/status"
	"github.com/mediactl/clustarr/captionarr/throttle"
	"github.com/mediactl/clustarr/captionarr/worker/fetch"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

var os1 = subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom

// workerItemLeaves is every SubtitleItem leaf captionarr-worker owns once it
// has recorded a download: everything but nextSearchAt and attempts.
var workerItemLeaves = []string{
	"downloadedAt", "langKey", "lastError", "path", "provider", "score", "scoreOutOf", "state", "subtitleID",
}

// The happy path, end to end through one delivery: the first provider in
// priority order answers with nothing good enough, so the worker moves on;
// the second's best candidate fails to download, so it falls through to the
// next one, which is post-processed with the profile's mods, written as a
// sidecar beside the video, recorded on the item and announced.
func TestFetchWritesTheBestAcceptableSubtitleAndRecordsIt(t *testing.T) {
	f := newFixture(t, natsBus(t))

	weak := newFakeProvider("fake-a")
	weak.cands = []subtitles.Candidate{candidate("a-web", "Film.2010.720p.WEB-DL.x264-OTHER")} // 101 < 126
	strong := newFakeProvider("fake-b")
	strong.cands = []subtitles.Candidate{
		candidate("b-exact", releaseTitle),                        // 147, but its download fails
		candidate("b-other", "Film.2010.1080p.BluRay.x264-OTHER"), // 132
	}
	strong.dlErr["b-exact"] = errors.New("the stored file is gone")
	strong.files["b-other"] = []byte(srtWithHI)
	a := f.entry("os-a", os1, weak)
	b := f.entry("os-b", os1, strong)

	msg := f.message(t, "en", nil)
	require.NoError(t, f.worker.Handle(f.ctx, msg))

	written, err := os.ReadFile(f.local(filepath.Join(filepath.Dir(mediaLogical), sidecarName)))
	require.NoError(t, err, "the sidecar must sit beside the video under SidecarName")
	assert.Contains(t, string(written), "Hello there.")
	st, err := os.Stat(f.local(filepath.Join(filepath.Dir(mediaLogical), sidecarName)))
	require.NoError(t, err)
	assert.Equal(t, fetch.DefaultSidecarMode, st.Mode().Perm(), "no RootFolder holds the file: the default mode")
	assert.NotContains(t, string(written), "MUSIC", "the profile's removeHI mod ran")

	it := f.item(t, "en")
	assert.Equal(t, subtitlev1alpha1.SubtitleItemUpgradable, it.State, "132 is below 180-3, so upgrades continue")
	assert.Equal(t, int32(132), it.Score)
	assert.Equal(t, int32(180), it.ScoreOutOf)
	assert.Equal(t, "os-b", it.Provider, "the SubtitleProvider's name, not the client's")
	assert.Equal(t, "b-other", it.SubtitleID)
	assert.Equal(t, sidecarName, it.Path, "relative to the media file's directory; catalogarr joins it")
	require.NotNil(t, it.DownloadedAt)
	assert.True(t, it.DownloadedAt.Time.Equal(now))
	assert.Empty(t, it.LastError)

	assert.Equal(t, int32(0), weak.downloads.Load(), "nothing of the weak provider's reached the minimum")
	assert.Equal(t, int32(2), strong.downloads.Load(), "fall-through to the second-best candidate")
	assert.Positive(t, msg.inProgress.Load(), "a long fetch heartbeats")

	evts := f.bus.subtitleEvents(t)
	require.Len(t, evts, 1)
	assert.Equal(t, events.ActionDownloaded, evts[0].Action)
	assert.Equal(t, "en", evts[0].LangKey)
	assert.Equal(t, "os-b", evts[0].Provider)
	assert.Equal(t, "b-other", evts[0].ProviderID)
	assert.Equal(t, int32(132), evts[0].Score)
	assert.Equal(t, filepath.Join(filepath.Dir(mediaLogical), sidecarName), evts[0].Path)

	// Both providers answered, so both have a success stamped in the shared
	// throttle table (R2: the KV, never SubtitleProvider.status).
	kv := f.bus.KV(events.BucketProviderThrottle)
	for _, e := range []string{a.UID, b.UID} {
		st, err := throttle.Get(f.ctx, kv, e)
		require.NoError(t, err)
		require.NotNil(t, st.LastSuccessAt, e)
		assert.False(t, st.Throttled(now))
	}

	live := f.get(t)
	assert.Equal(t, map[string][]string{"en": workerItemLeaves}, itemLeaves(t, live, fetch.FieldManager),
		"the worker owns exactly its leaves -- never nextSearchAt or attempts")
	assert.Equal(t, map[string][]string{"en": {"langKey", "nextSearchAt"}}, itemLeaves(t, live, k8s.ManagerCaptionarr),
		"the controller keeps the schedule that makes the item live")
}

// This is the test CLAUDE.md's "A lost update is not an SSA release" gotcha
// asks for: real second writers land DURING the search, and the worker's
// final apply must not roll them back.
//
// Two writers interleave, and they are not equally informative:
//
//   - A sibling fetch worker records "es" (downloaded, with a sidecar path).
//     It shares this worker's field manager, and status.RequestWorkerFields
//     re-declares every worker leaf of EVERY item, so an apply seeded from
//     the pre-search read would re-send es as "pending" with no path --
//     rolling back a written sidecar that catalogarr would then drop from
//     MediaFile.status.sidecars. Only the re-read before the apply prevents
//     that. This is the distinguishing assertion: with the re-read removed,
//     it fails (see the task report's falsification).
//   - The controller reschedules "en" (nextSearchAt, attempts). Those
//     leaves belong to a different manager and are never declared by the
//     worker, so they survive the apply with or without the re-read; they
//     are asserted here to pin the split, not as proof of the re-read.
func TestFetchDoesNotRollBackWhatAnotherWriterRecordedDuringTheSearch(t *testing.T) {
	f := newFixture(t, natsBus(t))

	// Steady state before the task: both items live and planned.
	f.seedLive(t, "en", "es")
	seeded := f.get(t)
	seeded.Status.ProbeHash = f.probe
	seeded.Status.Phase = subtitlev1alpha1.SubtitleRequestPhaseSearching
	require.NoError(t, status.PatchRequest(f.ctx, f.c, k8s.ManagerCaptionarr, seeded, nil))

	rescheduled := metav1.NewTime(now.Add(6 * time.Hour))
	siblingAt := metav1.NewTime(now.Add(-time.Second))
	p := newFakeProvider("fake")
	p.cands = []subtitles.Candidate{candidate("exact", releaseTitle)}
	p.files["exact"] = []byte(srtWithHI)
	p.onSearch = func() {
		// A sibling worker finishes "es" while this one is still searching.
		sib := f.get(t)
		for i := range sib.Status.Items {
			if sib.Status.Items[i].LangKey == "es" {
				it := &sib.Status.Items[i]
				it.State = subtitlev1alpha1.SubtitleItemDownloaded
				it.Score, it.ScoreOutOf = 170, 180
				it.Provider, it.SubtitleID, it.Path = "os-sibling", "sib-1", "Film (2010).es.srt"
				it.DownloadedAt = &siblingAt
			}
		}
		require.NoError(t, status.PatchRequest(f.ctx, f.c, k8s.ManagerCaptionarrWorker, sib, nil))

		// ...and the controller reschedules "en".
		ctl := f.get(t)
		for i := range ctl.Status.Items {
			if ctl.Status.Items[i].LangKey == "en" {
				ctl.Status.Items[i].NextSearchAt = &rescheduled
				ctl.Status.Items[i].Attempts = commonv1.Attempts{Initial: &schedule, Latest: &siblingAt, Count: 2}
			}
		}
		require.NoError(t, status.PatchRequest(f.ctx, f.c, k8s.ManagerCaptionarr, ctl, nil))
	}
	f.entry("os", os1, p)

	require.NoError(t, f.worker.Handle(f.ctx, f.message(t, "en", nil)))

	es := f.item(t, "es")
	assert.Equal(t, subtitlev1alpha1.SubtitleItemDownloaded, es.State, "the sibling's download was rolled back")
	assert.Equal(t, "Film (2010).es.srt", es.Path, "the sibling's sidecar path was rolled back")
	assert.Equal(t, int32(170), es.Score)
	assert.Equal(t, "os-sibling", es.Provider)
	require.NotNil(t, es.DownloadedAt)

	en := f.item(t, "en")
	assert.Equal(t, subtitlev1alpha1.SubtitleItemUpgradable, en.State)
	assert.Equal(t, int32(147), en.Score)
	assert.Equal(t, sidecarName, en.Path)
	require.NotNil(t, en.NextSearchAt)
	assert.True(t, en.NextSearchAt.Equal(&rescheduled), "the controller's reschedule must survive the worker's apply")
	assert.Equal(t, int32(2), en.Attempts.Count)

	live := f.get(t)
	assert.Equal(t, map[string][]string{"en": workerItemLeaves, "es": workerItemLeaves},
		itemLeaves(t, live, fetch.FieldManager))
	assert.Equal(t, map[string][]string{
		"en": {"attempts", "langKey", "nextSearchAt"},
		"es": {"langKey", "nextSearchAt"},
	}, itemLeaves(t, live, k8s.ManagerCaptionarr), "the split holds on managedFields, where an over-claim would show")
	assert.Equal(t, subtitlev1alpha1.SubtitleRequestPhaseSearching, live.Status.Phase, "the controller's request fields are untouched")
}

// Nothing reaching the minimum score is a result, not a failure to retry:
// recorded, acked, no sidecar, no event.
func TestFetchRecordsUnavailableAndAcksWhenNothingReachesTheMinimum(t *testing.T) {
	f := newFixture(t, natsBus(t))
	p := newFakeProvider("fake")
	p.cands = []subtitles.Candidate{
		candidate("web", "Film.2010.720p.WEB-DL.x264-OTHER"),                 // 101
		{ID: "fr", FetchID: "fr", Language: "fr", ReleaseInfo: releaseTitle}, // wrong language
	}
	f.entry("os", os1, p)

	require.NoError(t, f.worker.Handle(f.ctx, f.message(t, "en", nil)))

	it := f.item(t, "en")
	assert.Equal(t, subtitlev1alpha1.SubtitleItemUnavailable, it.State)
	assert.Contains(t, it.LastError, "minimum score 126/180 (best 101 from os)")
	assert.Empty(t, it.Path)
	assert.Equal(t, int32(0), p.downloads.Load())
	assert.Empty(t, f.bus.subtitleEvents(t), "unavailable is not in the event vocabulary")
	_, err := os.Stat(f.local(filepath.Join(filepath.Dir(mediaLogical), sidecarName)))
	assert.True(t, os.IsNotExist(err))
}

// An upgrade replaces a subtitle only with a strictly better one, and one
// that finds nothing better does not touch the object at all.
func TestAnUpgradeOnlyEverReplacesWithSomethingBetter(t *testing.T) {
	f := newFixture(t, natsBus(t))

	// On disk: a 147-point .ass the worker recorded earlier.
	oldRel := "Film (2010).en.ass"
	oldLocal := f.local(filepath.Join(filepath.Dir(mediaLogical), oldRel))
	require.NoError(t, os.WriteFile(oldLocal, []byte("old"), 0o644))
	earlier := metav1.NewTime(now.Add(-24 * time.Hour))
	seeded := f.get(t)
	it := &seeded.Status.Items[0] // "en", live from the fixture
	it.State, it.Score, it.ScoreOutOf = subtitlev1alpha1.SubtitleItemUpgradable, 147, 180
	it.Provider, it.SubtitleID, it.Path, it.DownloadedAt = "os", "exact", oldRel, &earlier
	require.NoError(t, status.PatchRequest(f.ctx, f.c, k8s.ManagerCaptionarrWorker, seeded, nil))

	p := newFakeProvider("fake")
	p.cands = []subtitles.Candidate{candidate("same", releaseTitle)} // 147 again: not better
	p.files["same"] = []byte(srtWithHI)
	f.entry("os", os1, p)
	upgrade := func(ft *schema.FetchTask) { ft.Upgrade, ft.MinScore = true, 148 }

	before := f.get(t).ResourceVersion
	require.NoError(t, f.worker.Handle(f.ctx, f.message(t, "en", upgrade)))
	assert.Equal(t, before, f.get(t).ResourceVersion, "nothing better: no write at all")
	assert.Equal(t, int32(0), p.downloads.Load())
	assert.Empty(t, f.bus.subtitleEvents(t))

	// Now a hash-corroborated candidate: 179 beats 147.
	p.cands = []subtitles.Candidate{{
		ID: "hash", FetchID: "hash", Language: "en", ReleaseInfo: releaseTitle,
		Matches: map[string]bool{subtitles.MatchHash: true},
	}}
	p.files["hash"] = []byte(srtWithHI)
	require.NoError(t, f.worker.Handle(f.ctx, f.message(t, "en", upgrade)))

	it = ptrTo(f.item(t, "en"))
	assert.Equal(t, subtitlev1alpha1.SubtitleItemDownloaded, it.State, "179 is within 3 of 180: no further upgrades")
	assert.Equal(t, int32(179), it.Score)
	assert.Equal(t, sidecarName, it.Path)
	_, err := os.Stat(oldLocal)
	assert.True(t, os.IsNotExist(err), "the superseded .ass is removed")

	evts := f.bus.subtitleEvents(t)
	require.Len(t, evts, 1)
	assert.Equal(t, events.ActionUpgraded, evts[0].Action)
	assert.Equal(t, int32(147), evts[0].PreviousScore)
	assert.Equal(t, int32(179), evts[0].Score)
}

// R2: a provider error is recorded in the shared KV throttle -- on a real
// NATS server, since the in-memory bus has no key grammar -- and never on
// SubtitleProvider.status. Once throttled, the provider is skipped without
// a search, and "every provider throttled" is recorded and acked, not
// redelivered.
func TestProviderErrorsGoToTheSharedThrottleNeverToSubtitleProviderStatus(t *testing.T) {
	f := newFixture(t, natsBus(t))

	sp := &subtitlev1alpha1.SubtitleProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "os", Namespace: f.ns},
		Spec:       subtitlev1alpha1.SubtitleProviderSpec{Type: os1, Enabled: ptr.To(true), Priority: 1},
	}
	require.NoError(t, f.c.Create(f.ctx, sp))
	p := newFakeProvider("fake")
	p.searchErr = &subtitles.ProviderError{Provider: "opensubtitlescom", Kind: subtitles.KindTooManyRequests}
	e := f.entry("os", os1, p)
	e.UID = string(sp.UID) // key the throttle by the real object's UID
	f.source.entries[0] = e

	require.NoError(t, f.worker.Handle(f.ctx, f.message(t, "en", nil)), "a provider error is a result, not a redelivery")
	it := f.item(t, "en")
	assert.Equal(t, subtitlev1alpha1.SubtitleItemFailed, it.State)
	assert.Contains(t, it.LastError, "TooManyRequests")
	evts := f.bus.subtitleEvents(t)
	require.Len(t, evts, 1)
	assert.Equal(t, events.ActionFailed, evts[0].Action)

	st, err := throttle.Get(f.ctx, f.bus.KV(events.BucketProviderThrottle), e.UID)
	require.NoError(t, err)
	require.NotNil(t, st.ThrottledUntil)
	assert.True(t, st.ThrottledUntil.Equal(now.Add(time.Minute)), "OpenSubtitles' TooManyRequests override is one minute")
	assert.Equal(t, subtitles.KindTooManyRequests, st.ThrottleReason)
	assert.Equal(t, int32(1), st.ErrorsLast120s)

	var live subtitlev1alpha1.SubtitleProvider
	require.NoError(t, f.c.Get(f.ctx, client.ObjectKeyFromObject(sp), &live))
	assert.Equal(t, subtitlev1alpha1.SubtitleProviderStatus{}, live.Status, "R2: the worker never writes SubtitleProvider.status")
	for _, mf := range live.ManagedFields {
		assert.NotEqual(t, string(fetch.FieldManager), mf.Manager)
	}

	// The next task finds the provider benched and does not search it.
	require.NoError(t, f.worker.Handle(f.ctx, f.message(t, "en", nil)))
	assert.Equal(t, int32(1), p.searches.Load(), "a throttled provider is skipped before a token is spent")
	it = f.item(t, "en")
	assert.Equal(t, subtitlev1alpha1.SubtitleItemFailed, it.State, "a throttled-everywhere run keeps the last real state")
	assert.Contains(t, it.LastError, "every eligible provider is throttled")
	assert.Contains(t, it.LastError, "os until ")
}

// Settlement: poison dead-letters at once, stale work acks without a
// write, a changed file redelivers after five minutes, and the final
// delivery records why instead of vanishing into the DLQ. None of these
// reaches a provider, so the in-memory bus is enough.
func TestFetchSettlement(t *testing.T) {
	f := newFixture(t, memBus(t))
	p := newFakeProvider("fake")
	f.entry("os", os1, p)

	t.Run("an undecodable payload dead-letters", func(t *testing.T) {
		err := f.worker.Handle(f.ctx, &fakeMessage{env: &events.Envelope{Schema: "subtitle.FetchTask.v1", Data: []byte("{")}})
		var d *events.DiscardError
		assert.True(t, errors.As(err, &d), "got %v", err)
	})
	t.Run("an invalid langKey dead-letters", func(t *testing.T) {
		err := f.worker.Handle(f.ctx, f.message(t, "en:forced:hi", nil))
		var d *events.DiscardError
		assert.True(t, errors.As(err, &d), "got %v", err)
	})
	t.Run("a replaced request acks without a write", func(t *testing.T) {
		before := f.get(t).ResourceVersion
		require.NoError(t, f.worker.Handle(f.ctx, f.message(t, "en", func(ft *schema.FetchTask) { ft.RequestRef.UID = "old-uid" })))
		assert.Equal(t, before, f.get(t).ResourceVersion)
	})
	t.Run("a language the profile no longer wants acks without a write", func(t *testing.T) {
		before := f.get(t).ResourceVersion
		require.NoError(t, f.worker.Handle(f.ctx, f.message(t, "de", nil)))
		assert.Equal(t, before, f.get(t).ResourceVersion)
	})
	t.Run("a file that changed since planning redelivers in five minutes", func(t *testing.T) {
		err := f.worker.Handle(f.ctx, f.message(t, "en", func(ft *schema.FetchTask) { ft.ProbeHash = "planned-against-another-file" }))
		var r *events.RetryError
		require.True(t, errors.As(err, &r), "got %v", err)
		assert.Equal(t, 5*time.Minute, r.After)
	})
	t.Run("the final delivery records the failure and acks", func(t *testing.T) {
		m := f.message(t, "en", func(ft *schema.FetchTask) { ft.ProbeHash = "planned-against-another-file" })
		m.attempt = 8 // captionarr-fetch-*'s MaxDeliver
		require.NoError(t, f.worker.Handle(f.ctx, m))
		it := f.item(t, "en")
		assert.Equal(t, subtitlev1alpha1.SubtitleItemFailed, it.State)
		assert.Contains(t, it.LastError, "gave up after 8 deliveries")
	})
	t.Run("a deleted request acks", func(t *testing.T) {
		m := f.message(t, "en", nil)
		require.NoError(t, f.c.Delete(f.ctx, f.get(t)))
		require.NoError(t, f.worker.Handle(f.ctx, m))
	})
	assert.Equal(t, int32(0), p.searches.Load(), "no settlement path reaches a provider")
}

func ptrTo[T any](v T) *T { return &v }

// Rule 3 of the item-liveness protocol (status.IsLive), through the real
// worker path: once the controller stops scheduling "es", the worker's next
// apply -- for a different language entirely -- releases its leaves on es,
// no manager owns the entry, and server-side apply deletes it. Before the
// protocol the worker re-declared every entry it had ever written, so a
// withdrawn language stayed on the object (and in MediaFile.status.sidecars)
// forever.
func TestTheWorkersNextApplyDeletesALanguageTheControllerWithdrew(t *testing.T) {
	f := newFixture(t, natsBus(t))
	f.seedLive(t, "en", "es")

	// es was fetched earlier: the worker owns a full set of leaves on it.
	withSub := f.get(t)
	for i := range withSub.Status.Items {
		if it := &withSub.Status.Items[i]; it.LangKey == "es" {
			it.State, it.Score, it.ScoreOutOf = subtitlev1alpha1.SubtitleItemDownloaded, 170, 180
			it.Provider, it.SubtitleID, it.Path = "os", "es-1", "Film (2010).es.srt"
		}
	}
	require.NoError(t, status.PatchRequest(f.ctx, f.c, k8s.ManagerCaptionarrWorker, withSub, nil))

	f.withdraw(t, "es")
	require.Contains(t, itemLeaves(t, f.get(t), fetch.FieldManager), "es",
		"setup: until the worker applies again it still holds the withdrawn es")

	p := newFakeProvider("fake")
	p.cands = []subtitles.Candidate{candidate("exact", releaseTitle)}
	p.files["exact"] = []byte(srtWithHI)
	f.entry("os", os1, p)
	require.NoError(t, f.worker.Handle(f.ctx, f.message(t, "en", nil)))

	live := f.get(t)
	require.Len(t, live.Status.Items, 1, "the withdrawn item must be deleted")
	assert.Equal(t, "en", live.Status.Items[0].LangKey)
	assert.Equal(t, subtitlev1alpha1.SubtitleItemUpgradable, live.Status.Items[0].State)
	for _, mgr := range []k8s.FieldManager{fetch.FieldManager, k8s.ManagerCaptionarr} {
		assert.NotContains(t, itemLeaves(t, live, mgr), "es", "%s still owns a leaf of the withdrawn item", mgr)
	}
	assert.Equal(t, map[string][]string{"en": workerItemLeaves}, itemLeaves(t, live, fetch.FieldManager))
}

// A task for a language the controller no longer schedules records nothing
// and acks -- whether the want was withdrawn before the task was picked up,
// or during the provider search. The worker never creates or revives an
// item.
func TestATaskForAWithdrawnLanguageRecordsNothingAndAcks(t *testing.T) {
	t.Run("withdrawn before delivery", func(t *testing.T) {
		f := newFixture(t, natsBus(t))
		f.seedLive(t, "en", "es")
		p := newFakeProvider("fake")
		f.entry("os", os1, p)
		m := f.message(t, "es", nil)
		f.withdraw(t, "es")

		before := f.get(t).ResourceVersion
		require.NoError(t, f.worker.Handle(f.ctx, m))
		assert.Equal(t, before, f.get(t).ResourceVersion, "nothing recorded")
		assert.Equal(t, int32(0), p.searches.Load(), "no provider is asked for a withdrawn want")
	})

	t.Run("never planned", func(t *testing.T) {
		f := newFixture(t, natsBus(t))
		p := newFakeProvider("fake")
		f.entry("os", os1, p)

		before := f.get(t).ResourceVersion
		require.NoError(t, f.worker.Handle(f.ctx, f.message(t, "es", nil)))
		live := f.get(t)
		assert.Equal(t, before, live.ResourceVersion)
		assert.NotContains(t, status.LiveItemKeys(live.Status), "es")
		assert.Len(t, live.Status.Items, 1, "the worker never creates an item")
	})

	t.Run("withdrawn during the search", func(t *testing.T) {
		f := newFixture(t, natsBus(t))
		p := newFakeProvider("fake")
		p.cands = []subtitles.Candidate{candidate("exact", releaseTitle)}
		p.files["exact"] = []byte(srtWithHI)
		var withdrawnAt string
		p.onSearch = func() {
			f.withdraw(t, "en")
			withdrawnAt = f.get(t).ResourceVersion
		}
		f.entry("os", os1, p)

		require.NoError(t, f.worker.Handle(f.ctx, f.message(t, "en", nil)))
		live := f.get(t)
		assert.Equal(t, withdrawnAt, live.ResourceVersion, "the controller spoke last; the worker recorded nothing")
		assert.NotContains(t, status.LiveItemKeys(live.Status), "en")
		assert.Empty(t, f.bus.subtitleEvents(t), "nothing recorded, nothing announced")
	})
}
