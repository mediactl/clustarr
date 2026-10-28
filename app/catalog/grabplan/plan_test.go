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

package grabplan_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
)

// TestGrabplanReChecksAgainstFreshState is ADR-0019 §6.6 step 2: the agent
// approved the release against the item as it was; the planner re-checks
// it against the item now. A release now queued, or now below a better
// imported file, is refused; the same release against the stale state is
// grabbed.
func TestGrabplanReChecksAgainstFreshState(t *testing.T) {
	c := movieCandidate(t, "Heat.1995.720p.HDTV.x264-GRP")

	t.Run("against the state the agent saw it is grabbed", func(t *testing.T) {
		p := grabplan.Decide(movieView(t, nil, c))
		require.Equal(t, []string{"Heat.1995.720p.HDTV.x264-GRP"}, titles(p))
	})
	t.Run("a grab of equal or better preference is now in flight", func(t *testing.T) {
		v := movieView(t, nil, c)
		v.Entries = []catalogv1alpha1.DownloadEntry{
			liveEntry(movieCandidate(t, "Heat.1995.1080p.BluRay.x264-OTHER"), commonv1.DownloadPhaseDownloading),
		}
		p := grabplan.Decide(v)
		require.Empty(t, p.NewEntries)
		require.Equal(t, 1, p.Decisions["overlapsLive"], "%+v", p.Decisions)
	})
	t.Run("a better file was imported since", func(t *testing.T) {
		p := grabplan.Decide(movieView(t, &decision.Current{Quality: q(t, "Bluray-1080p")}, c))
		require.Empty(t, p.NewEntries)
	})
	t.Run("a worse file is still upgraded", func(t *testing.T) {
		p := grabplan.Decide(movieView(t, &decision.Current{Quality: q(t, "SDTV")}, c))
		require.Len(t, p.NewEntries, 1)
	})
	t.Run("a finished grab no longer holds the queue", func(t *testing.T) {
		v := movieView(t, nil, c)
		v.Entries = []catalogv1alpha1.DownloadEntry{
			liveEntry(movieCandidate(t, "Heat.1995.1080p.BluRay.x264-OTHER"), commonv1.DownloadPhaseFailed),
		}
		require.Len(t, grabplan.Decide(v).NewEntries, 1)
	})
}

// A release its indexer kept refusing is given up without a block
// (releaseRefused), and its Failed entry is a tombstone while it is kept:
// the release is refused for the owner -- under another entry's id too --
// and another release is grabbed.
func TestARefusedReleaseIsNotGrabbedAgainWhileItsEntryStands(t *testing.T) {
	refused := movieCandidate(t, "Heat.1995.720p.HDTV.x264-GRP")
	other := movieCandidate(t, "Heat.1995.720p.HDTV.x264-OTHER")
	v := movieView(t, nil, refused)
	v.PendingCap = 0
	e := liveEntry(refused, commonv1.DownloadPhaseFailed)
	e.FailureReason = commonv1.DownloadFailureReleaseRefused
	e.ID = "heat-older-grab"
	v.Entries = []catalogv1alpha1.DownloadEntry{e}
	require.Empty(t, grabplan.Decide(v).NewEntries, "the refused release is not grabbed again")

	v = movieView(t, nil, refused, other)
	v.PendingCap = 0
	v.Entries = []catalogv1alpha1.DownloadEntry{e}
	p := grabplan.Decide(v)
	require.Len(t, p.NewEntries, 1)
	require.Equal(t, other.Release.Decision.GUID, p.NewEntries[0].Release.GUID)
}

// TestATombstoneRejectsACandidateFromBeforeTheBlock is ruling R7 and spec
// §6.14's "no regrab in the gap": a search answer computed before a block
// carries no Blocked mark, but the owner's Blocklisted tombstone of the
// same release (by info hash, or by indexer and GUID) refuses it, while
// another release is still grabbed.
func TestATombstoneRejectsACandidateFromBeforeTheBlock(t *testing.T) {
	blocked := movieCandidate(t, "Heat.1995.720p.HDTV.x264-GRP")
	require.Nil(t, blocked.Release.Blocked, "the answer predates the block")
	other := movieCandidate(t, "Heat.1995.720p.HDTV.x264-OTHER")

	v := movieView(t, nil, blocked)
	v.PendingCap = 0
	v.Entries = []catalogv1alpha1.DownloadEntry{liveEntry(blocked, commonv1.DownloadPhaseBlocklisted)}
	// The tombstone's id is another grab's (an older seq): the release is
	// refused for the block, not for already being an entry.
	v.Entries[0].ID = "heat-older-grab"
	p := grabplan.Decide(v)
	require.Empty(t, p.NewEntries, "a tombstoned release is never regrabbed")

	t.Run("by info hash from another indexer", func(t *testing.T) {
		mirror := blocked
		mirror.Release.Decision.ReleaseInfo.GUID = "another-indexers-guid"
		mirror.Release.Decision.ReleaseInfo.IndexerRef = "another-indexer"
		mirror.Source.IndexerDownload = &commonv1.IndexerDownload{IndexerRef: "another-indexer", GUID: "another-indexers-guid"}
		v := v
		v.Candidates = []grabplan.Candidate{mirror}
		require.Empty(t, grabplan.Decide(v).NewEntries)
	})
	t.Run("another release is grabbed", func(t *testing.T) {
		v := v
		v.Candidates = []grabplan.Candidate{blocked, other}
		require.Equal(t, []string{"Heat.1995.720p.HDTV.x264-OTHER"}, titles(grabplan.Decide(v)))
	})
}

// TestADelayedGrabIsMadeFromStatusAlone is P15-P18's retirement: a release
// the delay profile holds becomes a pending grab with grabAt and its
// candidate; once grabAt passes, the next pass grabs it from that status
// alone -- no record, no intake message, no scheduled grab.
func TestADelayedGrabIsMadeFromStatusAlone(t *testing.T) {
	c := movieCandidate(t, "Heat.1995.720p.HDTV.x264-GRP")
	v := movieView(t, nil, c)
	v.Delay = &catalogv1alpha1.DelayProfileSpec{TorrentDelayMinutes: 60, BypassIfHighestQuality: ptr.To(true)}

	first := grabplan.Decide(v)
	require.Empty(t, first.NewEntries, "the top tier is not met: the release waits out the delay")
	require.Len(t, first.Pending, 1)
	pg := first.Pending[0]
	require.Equal(t, now.Add(time.Hour), pg.GrabAt.UTC())
	require.NotNil(t, pg.Candidate)
	require.Equal(t, now.Add(time.Hour), first.Due)
	require.Equal(t, 1, first.Decisions["delayed"])

	// Before grabAt the pending grab is kept as it is.
	v.Candidates, v.Pending, v.Now = nil, first.Pending, now.Add(30*time.Minute)
	mid := grabplan.Decide(v)
	require.Empty(t, mid.NewEntries)
	require.Equal(t, first.Pending, mid.Pending)

	// After it, status alone makes the grab.
	v.Now = now.Add(61 * time.Minute)
	due := grabplan.Decide(v)
	require.Equal(t, []string{"Heat.1995.720p.HDTV.x264-GRP"}, titles(due))
	require.Empty(t, due.Pending, "the pending grab is consumed")
	e := due.NewEntries[0]
	require.Equal(t, lifecycle.EntryID("heat", "", c.Release.Decision.GUID), e.ID)
	require.Equal(t, commonv1.DownloadPhasePending, e.Phase)
	require.Equal(t, *c.Source.IndexerDownload, *e.Source.IndexerDownload)
	require.Equal(t, q(t, "HDTV-720p"), e.Release.Quality)

	t.Run("the top tier bypasses the delay", func(t *testing.T) {
		v := movieView(t, nil, movieCandidate(t, "Heat.1995.1080p.BluRay.x264-GRP"))
		v.Delay = &catalogv1alpha1.DelayProfileSpec{TorrentDelayMinutes: 60}
		require.Len(t, grabplan.Decide(v).NewEntries, 1)
	})
}

// TestAPackIsNarrowedToTheWantedEpisodes is P23: a pack covering some
// wanted episodes is taken as covering those (an episode whose file meets
// the cutoff does not want it); one covering none is refused. Rewritten for
// Wave S: a pack is never narrowed past a queued episode -- that refuses it
// whole (TestALiveGrabRefusesAnOverlappingReleaseUnlessItUpgrades).
func TestAPackIsNarrowedToTheWantedEpisodes(t *testing.T) {
	covered := season(1, 7)
	for i := range 3 {
		covered[i].Current = &decision.Current{Quality: q(t, "Bluray-1080p")}
	}
	cands := seasonCandidates(t, 1, 7, "Breaking.Bad.S01.720p.HDTV.x264-GRP")
	p := grabplan.Decide(seriesView(t, covered, cands...))
	require.Len(t, p.NewEntries, 1)
	require.Equal(t, []catalogv1alpha1.EpisodeNumber{ep(1, 4), ep(1, 5), ep(1, 6), ep(1, 7)}, p.NewEntries[0].Episodes)

	t.Run("a pack covering no wanted episode is refused", func(t *testing.T) {
		covered := season(1, 7)
		for i := range covered {
			covered[i].Current = &decision.Current{Quality: q(t, "Bluray-1080p")}
		}
		p := grabplan.Decide(seriesView(t, covered, cands...))
		require.Empty(t, p.NewEntries)
		require.Equal(t, 1, p.Decisions["refused"])
	})
	t.Run("an unmonitored episode is narrowed out", func(t *testing.T) {
		covered := season(1, 7)
		covered[6].Monitored = false
		p := grabplan.Decide(seriesView(t, covered, cands...))
		require.Len(t, p.NewEntries, 1)
		require.NotContains(t, p.NewEntries[0].Episodes, ep(1, 7))
		require.Len(t, p.NewEntries[0].Episodes, 6)
	})
}

// TestAManualPickIsExemptFromTheBaselineRules: an automatic candidate never
// replaces an HDR HEVC file with an SDR AVC release (LosesHDR, AVCOverHEVC,
// 2026-10-16), and a person's pick still may, as it may over a transcoded
// file.
func TestAManualPickIsExemptFromTheBaselineRules(t *testing.T) {
	c := movieCandidate(t, "Heat.1995.1080p.BluRay.x264-GRP")
	current := func(codec string, hdr commonv1.HdrFormat) *decision.Current {
		return &decision.Current{Quality: q(t, "HDTV-720p"), AudioLanguages: []string{"en"}, VideoCodec: codec, HDR: hdr}
	}

	unprobed := grabplan.Decide(movieView(t, current("", ""), parked(heatRef, "rss-0", c, false, false)))
	require.Len(t, unprobed.NewEntries, 1, "the control: the release upgrades an unprobed file")

	auto := grabplan.Decide(movieView(t, current("hevc", commonv1.HdrFormatHDR10), parked(heatRef, "rss-1", c, false, false)))
	require.Empty(t, auto.NewEntries, "an automatic candidate never replaces an HDR HEVC file with SDR AVC")
	require.Equal(t, intake.Refused, auto.Settle["rss-1"])

	pick := grabplan.Decide(movieView(t, current("hevc", commonv1.HdrFormatHDR10), parked(heatRef, "pick-1", c, true, false)))
	require.Len(t, pick.NewEntries, 1, "a person's pick is exempt from the baseline rules")
	require.Equal(t, intake.Incorporated, pick.Settle["pick-1"])
}

// TestAManualPickIsExemptFromTranscodedFinalAndOverrideFromBlocklisted is
// resolveGrab's rule in the planner: a person's pick takes a release over a
// transcoded file (TranscodedFinal is the automatic rule), and only
// spec.override takes a release the owner blocklisted.
func TestAManualPickIsExemptFromTranscodedFinalAndOverrideFromBlocklisted(t *testing.T) {
	c := movieCandidate(t, "Heat.1995.1080p.BluRay.x264-GRP")
	transcoded := &decision.Current{Quality: q(t, "HDTV-720p"), Transcoded: true}

	auto := grabplan.Decide(movieView(t, transcoded, parked(heatRef, "rss-1", c, false, false)))
	require.Empty(t, auto.NewEntries, "an automatic candidate never replaces a transcoded file")
	require.Equal(t, intake.Refused, auto.Settle["rss-1"])

	pick := grabplan.Decide(movieView(t, transcoded, parked(heatRef, "pick-1", c, true, false)))
	require.Len(t, pick.NewEntries, 1, "a person's pick is exempt from TranscodedFinal")
	require.True(t, pick.NewEntries[0].Manual)
	require.Equal(t, commonv1.GrabSourceInteractive, pick.NewEntries[0].GrabbedBy)
	require.Equal(t, intake.Incorporated, pick.Settle["pick-1"])

	tombstoned := func(cand grabplan.Candidate) grabplan.View {
		v := movieView(t, nil, cand)
		e := liveEntry(c, commonv1.DownloadPhaseBlocklisted)
		e.ID = "heat-older-grab"
		v.Entries = []catalogv1alpha1.DownloadEntry{e}
		return v
	}
	manual := grabplan.Decide(tombstoned(parked(heatRef, "pick-2", c, true, false)))
	require.Empty(t, manual.NewEntries, "a pick does not pass a block")
	require.Equal(t, intake.Refused, manual.Settle["pick-2"])

	override := grabplan.Decide(tombstoned(parked(heatRef, "pick-3", c, true, true)))
	require.Len(t, override.NewEntries, 1, "spec.override takes a blocklisted release")
	require.Equal(t, intake.Incorporated, override.Settle["pick-3"])

	t.Run("override takes the very grab the owner blocklisted", func(t *testing.T) {
		// The tombstone is the earlier grab of this release, so it carries
		// the id a new grab of it would get.
		v := movieView(t, nil, parked(heatRef, "pick-4", c, true, true))
		e := liveEntry(c, commonv1.DownloadPhaseBlocklisted)
		e.ID = lifecycle.EntryID("heat", "", c.Release.Decision.GUID)
		v.Entries = []catalogv1alpha1.DownloadEntry{e}
		p := grabplan.Decide(v)
		require.Len(t, p.NewEntries, 1, "spec.override must take the blocklisted release: %+v", p.Decisions)
	})
}

// TestAnOwnerAtItsCapPendsNeverTruncates is §6.6 step 2: an owner holding
// EntryCap entries refuses a grab with one Normal AtCapacity Event, never
// dropping an entry it holds; a release the delay profile holds is still
// pended, since a pending grab is no entry.
func TestAnOwnerAtItsCapPendsNeverTruncates(t *testing.T) {
	c := parked(heatRef, "rss-1", movieCandidate(t, "Heat.1995.1080p.BluRay.x264-GRP"), false, false)
	v := movieView(t, nil, c)
	v.EntryCap = 2
	v.Entries = []catalogv1alpha1.DownloadEntry{
		liveEntry(movieCandidate(t, "Heat.1995.720p.HDTV.x264-A"), commonv1.DownloadPhaseImported),
		liveEntry(movieCandidate(t, "Heat.1995.720p.HDTV.x264-B"), commonv1.DownloadPhaseImported),
	}
	p := grabplan.Decide(v)
	require.Empty(t, p.NewEntries)
	require.Equal(t, intake.Refused, p.Settle["rss-1"])
	require.Len(t, p.Events, 1)
	require.Equal(t, grabplan.ReasonAtCapacity, p.Events[0].Reason)
	require.Equal(t, lifecycle.EventNormal, p.Events[0].Type)

	t.Run("a delayed release is pended at the cap", func(t *testing.T) {
		v := v
		v.Candidates = []grabplan.Candidate{parked(heatRef, "rss-2", movieCandidate(t, "Heat.1995.720p.HDTV.x264-GRP"), false, false)}
		v.Delay = &catalogv1alpha1.DelayProfileSpec{TorrentDelayMinutes: 60}
		p := grabplan.Decide(v)
		require.Empty(t, p.NewEntries)
		require.Len(t, p.Pending, 1)
		require.Equal(t, intake.Pended, p.Settle["rss-2"])
		require.Empty(t, p.Events)
	})
}

// TestStillWantsKeepsAnUpgradeAndAnUnmetSeedObligation is Review Focus 3:
// an unclaimed transfer is kept while the owner still wants it -- its
// release is still an upgrade of the current file, or (lifecycle's own
// rule, ahead of StillWants) it was imported and its seed goal is not met
// -- and removed once neither holds.
func TestStillWantsKeepsAnUpgradeAndAnUnmetSeedObligation(t *testing.T) {
	claim := schema.TransferClaim{
		Owner: heatRef, Entry: schema.EntryRef{ID: "heat-grab", UID: "uid-grab"},
		InfoHash: "0123456789abcdef0123456789abcdef01234567", Indexer: indexer, GUID: "g1",
		Title: "Heat.1995.1080p.BluRay.x264-GRP",
	}
	wants := func(cur *decision.Current) bool {
		ok, _ := grabplan.StillWants(movieView(t, cur), claim)
		return ok
	}
	require.True(t, wants(nil), "no file")
	require.True(t, wants(&decision.Current{Quality: q(t, "HDTV-720p")}), "still an upgrade over the current file")
	require.False(t, wants(&decision.Current{Quality: q(t, "Bluray-1080p")}), "a file at the release's quality")
	require.False(t, wants(&decision.Current{Quality: q(t, "HDTV-720p"), Transcoded: true}), "a transcoded file is final")

	// A file outside the profile's bitrate range is still replaced, a
	// transcode included, by a release of any quality the profile takes
	// (2026-10-27): here a WEB-first profile's Blu-ray tier.
	webdl, ok := quality.Lookup("video", "WEBDL-1080p")
	require.True(t, ok)
	bluray, ok := quality.Lookup("video", "Bluray-1080p")
	require.True(t, ok)
	limited := func(cur *decision.Current) bool {
		v := movieView(t, cur)
		prof := *v.Profile
		prof.Tiers = [][]quality.Definition{{webdl}, {bluray}}
		prof.CutoffIndex = 1
		prof.Bitrates = map[int32]quality.BitrateLimit{1080: {Min: 100_000, Max: 1_000_000}}
		v.Profile = &prof
		ok, _ := grabplan.StillWants(v, claim)
		return ok
	}
	require.True(t, limited(&decision.Current{Quality: q(t, "Bluray-1080p"), Transcoded: true, BytesPerSecond: 67_000}),
		"a transcode below the range")
	require.True(t, limited(&decision.Current{Quality: q(t, "WEBDL-1080p"), Transcoded: true, BytesPerSecond: 67_000}),
		"a WEB-DL below the range, replaced from the lower Blu-ray tier")
	require.False(t, limited(&decision.Current{Quality: q(t, "Bluray-1080p"), Transcoded: true, BytesPerSecond: 400_000}),
		"a transcode inside the range is final")

	v := movieView(t, nil)
	v.Monitored = false
	ok, _ = grabplan.StillWants(v, claim)
	require.False(t, ok, "an unmonitored owner")

	// The seed obligation, through lifecycle with the planner bound as its
	// StillWants: the owner has the file at the release's quality, so only
	// the unmet seed goal keeps the transfer.
	converge := func(imported, seeded bool) lifecycle.Plan {
		cl := claim
		cl.Imported = imported
		gv := movieView(t, &decision.Current{Quality: q(t, "Bluray-1080p")})
		return lifecycle.Decide(lifecycle.View{
			Owner: heatRef, Kind: commonv1.MediaKindMovie, Now: now, Monitored: true, EntryCap: 100,
			Clients: []lifecycle.Client{{Name: "qbit", Protocol: commonv1.ProtocolTorrent, Enabled: true, Replicas: 1}},
			Unclaimed: []lifecycle.Transfer{{Record: schema.TransferRecord{
				RecordHeader: schema.RecordHeader{Item: &heatRef, State: schema.TransferStatePresent, Seq: 3},
				Entry:        cl.Entry, Engine: "qbit-0", Claim: &cl,
				Stage: commonv1.DownloadStageSeeding, SeedGoalReached: seeded,
			}}},
			StillWants: func(c schema.TransferClaim) (bool, string) { return grabplan.StillWants(gv, c) },
		})
	}
	kept := converge(true, false)
	require.Len(t, kept.Entries, 1, "an imported transfer short of its seed goal is re-created")
	require.Equal(t, "uid-grab", kept.Entries[0].UID)
	for _, c := range kept.Commands {
		require.NotEqual(t, schema.EngineDesiredAbsent, c.Cmd.Desired, "nothing removes the kept transfer")
	}

	removed := converge(true, true)
	require.Empty(t, removed.Entries, "its seed goal met, nothing wants it")
	require.Len(t, removed.Commands, 1)
	require.Equal(t, schema.EngineDesiredAbsent, removed.Commands[0].Cmd.Desired)
}

// TestTheQuarantineTombstoneRefusesAutomaticGrabsButNotAnOverride is the
// smoke test's T2 finding: a release's Blocklisted grab stays a tombstone
// for lifecycle.BlockQuarantine after its transfer is gone, under the very
// id a new grab of the release gets (lifecycle.EntryID), so a person's
// spec.override pick of it read as "already a grab of the owner" for that
// hour. The override is admitted as a new entry, named after the tombstone
// it overrides, beside it; the tombstone still refuses every automatic
// answer, and a person's pick without override.
func TestTheQuarantineTombstoneRefusesAutomaticGrabsButNotAnOverride(t *testing.T) {
	c := movieCandidate(t, "Heat.1995.1080p.BluRay.x264-GRP")
	tomb := liveEntry(c, commonv1.DownloadPhaseBlocklisted)
	tomb.ID = lifecycle.EntryID("heat", "", c.Release.Decision.GUID)
	confirmed := metav1.NewTime(now.Add(-10 * time.Minute)) // inside lifecycle.BlockQuarantine
	tomb.Block = &catalogv1alpha1.EntryBlock{
		Scope: catalogv1alpha1.BlockScopeItem, Reason: commonv1.DownloadFailureImportRejected, Seq: 1, ConfirmedAt: &confirmed,
	}
	view := func(cand grabplan.Candidate, more ...catalogv1alpha1.DownloadEntry) grabplan.View {
		v := movieView(t, nil, cand)
		v.Entries = append([]catalogv1alpha1.DownloadEntry{tomb}, more...)
		return v
	}

	for name, cand := range map[string]grabplan.Candidate{
		"a search answer":                  c,
		"an RSS match":                     parked(heatRef, "rss-1", c, false, false),
		"a grabBest":                       parked(heatRef, "best-1", c, false, false),
		"a person's pick without override": parked(heatRef, "pick-1", c, true, false),
	} {
		p := grabplan.Decide(view(cand))
		require.Empty(t, p.NewEntries, "%s is refused in the quarantine", name)
		if cand.MsgID != "" {
			require.Equal(t, intake.Refused, p.Settle[cand.MsgID], name)
		}
	}

	p := grabplan.Decide(view(parked(heatRef, "pick-2", c, true, true)))
	require.Len(t, p.NewEntries, 1, "spec.override takes the release in its quarantine: %+v", p.Decisions)
	e := p.NewEntries[0]
	require.NotEqual(t, tomb.ID, e.ID, "a new entry, beside the tombstone")
	require.NotEqual(t, tomb.UID, e.UID)
	require.Equal(t, lifecycle.OverrideEntryID(tomb.ID, tomb.UID), e.ID, "named after the tombstone it overrides")
	require.True(t, e.Manual)
	require.Equal(t, c.Release.Decision.GUID, e.Release.GUID)
	require.Equal(t, intake.Incorporated, p.Settle["pick-2"])

	t.Run("the override is grabbed once", func(t *testing.T) {
		p := grabplan.Decide(view(parked(heatRef, "pick-2", c, true, true), e))
		require.Empty(t, p.NewEntries, "a redelivered override finds its grab")
		require.Equal(t, intake.Refused, p.Settle["pick-2"])
	})
	t.Run("beside the override's grab, an automatic answer is still refused", func(t *testing.T) {
		live := e
		live.Phase = commonv1.DownloadPhaseDownloading
		require.Empty(t, grabplan.Decide(view(parked(heatRef, "rss-2", c, false, false), live)).NewEntries)
	})
	t.Run("an override of the override's own tombstone is another new entry", func(t *testing.T) {
		blocked := e
		blocked.Phase = commonv1.DownloadPhaseBlocklisted
		p := grabplan.Decide(view(parked(heatRef, "pick-3", c, true, true), blocked))
		require.Len(t, p.NewEntries, 1, "%+v", p.Decisions)
		require.Equal(t, lifecycle.OverrideEntryID(blocked.ID, blocked.UID), p.NewEntries[0].ID)
		require.NotContains(t, []string{tomb.ID, e.ID}, p.NewEntries[0].ID)
	})
	t.Run("lifecycle admits the override's entry beside the tombstone", func(t *testing.T) {
		got := lifecycle.Decide(lifecycle.View{
			Owner: heatRef, Now: now, Stored: []catalogv1alpha1.DownloadEntry{tomb}, NewEntries: []catalogv1alpha1.DownloadEntry{e},
			EntryCap: 100,
		})
		var ids []string
		for _, x := range got.Entries {
			ids = append(ids, x.ID)
		}
		require.Contains(t, ids, e.ID, "the person's grab is admitted")
		require.Contains(t, ids, tomb.ID, "the tombstone keeps its quarantine")
	})
}

// Under a profile preferring the smallest, the smaller of two releases equal
// in tier, protocol and score is grabbed (owner, 2026-10-28).
func TestTheSmallestIsGrabbedUnderASmallestFirstProfile(t *testing.T) {
	big := movieCandidate(t, "Heat.1995.1080p.BluRay.x265-BIG")
	small := movieCandidate(t, "Heat.1995.1080p.WEB-DL.x265-SMALL")
	big.Release.Decision.ReleaseInfo.SizeBytes = 14_000_000_000
	small.Release.Decision.ReleaseInfo.SizeBytes = 3_000_000_000
	big.Release.Score, small.Release.Score = 100, 100
	v := movieView(t, nil, big, small)
	v.Profile.Tiers = [][]quality.Definition{append(append([]quality.Definition{}, v.Profile.Tiers[0]...), v.Profile.Tiers[1]...)}
	v.Profile.PreferSmallest = true
	require.Equal(t, []string{"Heat.1995.1080p.WEB-DL.x265-SMALL"}, titles(grabplan.Decide(v)))
}

// Under an audio policy's completeFirst the grab planner takes a release
// carrying every wanted language over a better-scored one that lacks a
// language, as decision.Rank does (owner, 2026-10-28: dual audio is always
// taken when it exists).
func TestCompleteAudioIsGrabbedFirstUnderCompleteFirst(t *testing.T) {
	decide := func(completeFirst bool) []string {
		japanese := movieCandidate(t, "Heat.1995.1080p.BluRay.x265-JPONLY")
		dual := movieCandidate(t, "Heat.1995.1080p.BluRay.DUAL.x264-DUALGRP")
		japanese.Release.Score, dual.Release.Score = 300, 0
		v := movieView(t, nil, japanese, dual)
		v.OriginalLanguageTag = "ja"
		v.Profile.AudioLanguages, v.Profile.GraftAudio = []string{"en", "original"}, true
		v.Profile.AudioCompleteFirst = completeFirst
		return titles(grabplan.Decide(v))
	}
	require.Equal(t, []string{"Heat.1995.1080p.BluRay.DUAL.x264-DUALGRP"}, decide(true))
	require.Equal(t, []string{"Heat.1995.1080p.BluRay.x265-JPONLY"}, decide(false), "without it the score decides")
}

// A search answer outlives the window the planner takes its candidates in:
// clustarr-searches retires a record events.SearchesTTL after its write.
func TestSearchRecordsOutliveTheCandidateWindow(t *testing.T) {
	require.GreaterOrEqual(t, events.SearchesTTL, grabplan.CandidateWindow)
}
