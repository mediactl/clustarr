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

package iptvprovider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"

	clustarrac "github.com/mediactl/clustarr/api/applyconfiguration/clustarr/v1alpha1"
	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
	"github.com/mediactl/clustarr/pkg/iptv/lineup"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
)

// TestAServerIsHTTPSUnlessItSaysOtherwise: spec.server takes a host, a
// host:port or a URL; with no scheme it is https, and anything that would
// carry a credential or a query is refused.
func TestAServerIsHTTPSUnlessItSaysOtherwise(t *testing.T) {
	for in, want := range map[string]string{
		"tvload.win":                   "https://tvload.win",
		" tvload.win/ ":                "https://tvload.win",
		"tvload.win:8080":              "https://tvload.win:8080",
		"http://tvload.win":            "http://tvload.win",
		"http://tvload.win:80/":        "http://tvload.win:80",
		"https://panel.example/xtream": "https://panel.example/xtream",
	} {
		got, err := NormalizeServer(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "ftp://tvload.win", "https://u:p@tvload.win", "tvload.win/get.php?username=u", "https://"} {
		_, err := NormalizeServer(bad)
		assert.Error(t, err, bad)
	}
}

// TestAStatusTheTunerCannotAnswerIsKept: before the tuner reports (a
// manager just started), the status re-sends what it says, the account and
// the lineup's span included, so the apply releases none of it.
func TestAStatusTheTunerCannotAnswerIsKept(t *testing.T) {
	exp := metav1.NewTime(time.Date(2026, 10, 21, 6, 19, 39, 0, time.UTC))
	p := &clustarrv1.IPTVProvider{Status: clustarrv1.IPTVProviderStatus{
		Account: &clustarrv1.AccountStatus{Status: "Active", ExpiresAt: &exp, MaxConnections: 1},
		Guide:   &clustarrv1.GuideStatus{Channels: 11165, Programmes: 9000},
		Lineup:  &clustarrv1.LineupStatus{Active: 243, First: "1001", Last: "1300"},
	}}
	st := clustarrac.IPTVProviderStatus()
	reassertKnownStatus(st, p)
	require.NotNil(t, st.Account)
	assert.Equal(t, "Active", *st.Account.Status)
	assert.Equal(t, exp, *st.Account.ExpiresAt)
	assert.Equal(t, int32(1), *st.Account.MaxConnections)
	require.NotNil(t, st.Guide)
	assert.Equal(t, int32(11165), *st.Guide.Channels)
	require.NotNil(t, st.Lineup)
	assert.Equal(t, "1001", *st.Lineup.First)
	assert.Equal(t, "1300", *st.Lineup.Last)
}

func TestATunerThatHasNotFetchedKeepsTheBlocksHashes(t *testing.T) {
	// After a manager restart the tuner renders before its first fetch,
	// with no hashes. Releasing them would make cluster-plex save every
	// block's map and reload its guide again.
	p := &clustarrv1.IPTVProvider{Status: clustarrv1.IPTVProviderStatus{Blocks: []clustarrv1.BlockStatus{
		{Start: "1001", LineupHash: "lineup-1001", GuideHash: "guide-1001"},
	}}}
	pl := plan{blocks: []clustarrv1.BlockStatus{{Start: "1001", Channels: 1, First: "1001", Last: "1001"}}}
	snap := tuner.Snapshot{Blocks: []tuner.BlockState{{Start: "1001"}}}

	st := clustarrac.IPTVProviderStatus()
	withBlocks(st, p, pl, snap, true)
	require.Len(t, st.Blocks, 1)
	require.NotNil(t, st.Blocks[0].LineupHash, "the lineup hash is released")
	require.NotNil(t, st.Blocks[0].GuideHash, "the guide hash is released")
	assert.Equal(t, "lineup-1001", *st.Blocks[0].LineupHash)
	assert.Equal(t, "guide-1001", *st.Blocks[0].GuideHash)

	snap.PlaylistFetched, snap.Blocks[0].LineupHash, snap.Blocks[0].GuideHash = true, "lineup-new", "guide-new"
	st = clustarrac.IPTVProviderStatus()
	withBlocks(st, p, pl, snap, true)
	assert.Equal(t, "lineup-new", *st.Blocks[0].LineupHash, "once fetched, the render's")
	assert.Equal(t, "guide-new", *st.Blocks[0].GuideHash)
}

// TestABlockTheTunerDoesNotServeYetIsNotPublished: a plan that moves a block
// onto a device the tuner's render does not serve yet (argon-tv onto Plex's
// own guide, 2026-11-04) keeps the status's blocks until the render lands,
// so cluster-plex never registers a device PMS cannot fetch, nor deletes
// the old DVR before the new device is there.
func TestABlockTheTunerDoesNotServeYetIsNotPublished(t *testing.T) {
	old := clustarrv1.BlockStatus{Start: "1001", DeviceID: "8246E54A", Path: "/hdhr/8246E54A", URI: "http://livetv/hdhr/8246E54A", LineupHash: "l-old", GuideHash: "g-old", Channels: 257}
	p := &clustarrv1.IPTVProvider{Status: clustarrv1.IPTVProviderStatus{Blocks: []clustarrv1.BlockStatus{old}}}
	pl := plan{blocks: []clustarrv1.BlockStatus{{Start: "1001", DeviceID: "1AED97B8", Path: "/hdhr/1AED97B8", URI: "http://livetv/hdhr/1AED97B8", Lineup: "lineup://tv.plex.providers.epg.cloud/657c8183afd0f02cd353633f", Channels: 167}}}

	// The render still serves the XMLTV device at the same start.
	snap := tuner.Snapshot{PlaylistFetched: true, Blocks: []tuner.BlockState{{Start: "1001", DeviceID: "8246E54A", LineupHash: "l-old", GuideHash: "g-old"}}}
	st := clustarrac.IPTVProviderStatus()
	withBlocks(st, p, pl, snap, true)
	require.Len(t, st.Blocks, 1)
	assert.Equal(t, "8246E54A", *st.Blocks[0].DeviceID, "the served device stays published")
	assert.Equal(t, "http://livetv/hdhr/8246E54A", *st.Blocks[0].URI)
	assert.Nil(t, st.Blocks[0].Lineup)
	assert.Equal(t, int32(257), *st.Blocks[0].Channels)

	// The render landed: the Plex device is served, and published.
	snap.Blocks = []tuner.BlockState{{Start: "1001", DeviceID: "1AED97B8", LineupHash: "l-plex"}}
	st = clustarrac.IPTVProviderStatus()
	withBlocks(st, p, pl, snap, true)
	require.Len(t, st.Blocks, 1)
	assert.Equal(t, "1AED97B8", *st.Blocks[0].DeviceID)
	assert.Equal(t, "lineup://tv.plex.providers.epg.cloud/657c8183afd0f02cd353633f", *st.Blocks[0].Lineup)
	assert.Equal(t, "l-plex", *st.Blocks[0].LineupHash)
	assert.Nil(t, st.Blocks[0].GuideHash, "a Plex block has no guide of its own")

	// A tuner that has not rendered a playlist (a manager just started)
	// publishes the plan, as before.
	st = clustarrac.IPTVProviderStatus()
	withBlocks(st, p, pl, tuner.Snapshot{}, true)
	require.Len(t, st.Blocks, 1)
	assert.Equal(t, "1AED97B8", *st.Blocks[0].DeviceID)
}

// TestABlockCarriesItsPathAndURI: the status names each block's device path
// and the URI Plex registers.
func TestABlockCarriesItsPathAndURI(t *testing.T) {
	pl := plan{blocks: []clustarrv1.BlockStatus{{
		Start: "1001", DeviceID: "CAFE0001", Path: "/hdhr/CAFE0001",
		URI: "http://clustarr-livetv.media.svc.cluster.local:80/hdhr/CAFE0001",
	}}}
	st := clustarrac.IPTVProviderStatus()
	withBlocks(st, &clustarrv1.IPTVProvider{}, pl, tuner.Snapshot{}, false)
	require.Len(t, st.Blocks, 1)
	require.NotNil(t, st.Blocks[0].Path)
	require.NotNil(t, st.Blocks[0].URI)
	assert.Equal(t, "/hdhr/CAFE0001", *st.Blocks[0].Path)
	assert.Equal(t, "http://clustarr-livetv.media.svc.cluster.local:80/hdhr/CAFE0001", *st.Blocks[0].URI)
}

var validCondition = metav1.Condition{Type: clustarrv1.ConditionValid, Status: metav1.ConditionTrue, Reason: "Valid"}

// servedSnapshot is a tuner that has rendered pl's devices.
func servedSnapshot(pl plan) tuner.Snapshot {
	s := tuner.Snapshot{PlaylistFetched: true, Guide: tuner.GuideState{Channels: 1, FetchedAt: time.Now()}}
	for _, b := range pl.blocks {
		s.Blocks = append(s.Blocks, tuner.BlockState{Start: b.Start, DeviceID: b.DeviceID})
	}
	return s
}

func condition(st *clustarrac.IPTVProviderStatusApplyConfiguration, typ string) *metav1ac.ConditionApplyConfiguration {
	for i := range st.Conditions {
		if *st.Conditions[i].Type == typ {
			return &st.Conditions[i]
		}
	}
	return nil
}

// TestCategoryStatusMirrorsThePlanAndTooMany: status.categories lists the
// plan's categories in its order, each with its base, its active channels
// counted from the channels' status.lineup.category, and its block's device;
// a category the plan refuses as TooMany says so and has no block, and the
// LineupApplied condition names it with the planner's count.
func TestCategoryStatusMirrorsThePlanAndTooMany(t *testing.T) {
	p := lineupProvider(true)
	p.Generation = 3
	inactive := catChannel("1004", "News")
	inactive.Spec.Active = false
	chans := []clustarrv1.IPTVChannel{
		catChannel("1001", "News"), catChannel("1002", "News"), catChannel("1003", "News"), inactive,
		catChannel("2001", "Sports"), catChannel("2002", "Sports"),
		catChannel("4001", "Movies"), // a channel the lineup controller has not deactivated yet
		catChannel("1500", ""),
	}
	r := fakeReconciler(t, testAddress, p)
	r.PlanReader = storedPlan(appliedPlanFor(chans,
		lineup.Category{Name: "News", Base: 1000, Channels: 3},
		lineup.Category{Name: "Sports", Base: 2000, Channels: 2},
		lineup.Category{Name: "Movies", Base: 4000, Channels: 512, TooMany: true},
		lineup.Category{Name: "Kids", Base: 5000},
	))
	pl, err := r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	require.Len(t, pl.blocks, 2, "no block for the refused category")
	tn := &snapTuner{snap: servedSnapshot(pl), ok: true}

	ac := render(p, testAddress, pl, tn, validCondition, resolved{}, false, time.Now())
	st := ac.Status
	require.Len(t, st.Categories, 4)
	type row struct {
		name             string
		base, channels   int32
		deviceID, reason string
	}
	got := make([]row, 0, len(st.Categories))
	for _, c := range st.Categories {
		r := row{name: *c.Name, base: *c.Base, channels: *c.Channels}
		if c.DeviceID != nil {
			r.deviceID = *c.DeviceID
		}
		if c.Reason != nil {
			r.reason = *c.Reason
		}
		got = append(got, r)
	}
	assert.Equal(t, []row{
		{"News", 1000, 3, hdhr.BlockDeviceID("media", lineupProviderName, "cat/News"), ""},
		{"Sports", 2000, 2, hdhr.BlockDeviceID("media", lineupProviderName, "cat/Sports"), ""},
		{"Movies", 4000, 1, "", clustarrv1.ReasonTooMany},
		{"Kids", 5000, 0, "", ""},
	}, got)

	require.Len(t, st.Blocks, 2)
	assert.Equal(t, "News", *st.Blocks[0].Category)
	assert.Equal(t, "Sports", *st.Blocks[1].Category)

	c := condition(st, clustarrv1.ConditionLineupApplied)
	require.NotNil(t, c)
	assert.Equal(t, metav1.ConditionFalse, *c.Status)
	assert.Equal(t, clustarrv1.ReasonTooMany, *c.Reason)
	assert.Contains(t, *c.Message, "Movies")
	assert.Contains(t, *c.Message, "512")
	assert.Equal(t, int64(3), *c.ObservedGeneration)
	assert.Equal(t, metav1.ConditionTrue, *condition(st, clustarrv1.ConditionReady).Status, "a refused category is the lineup's, not the provider's")
}

// TestTheLineupAppliedCondition: only while spec.lineup is set, from the
// stored plan: Pending before one, InvalidLineup with its error, DryRun,
// Applied.
func TestTheLineupAppliedCondition(t *testing.T) {
	news := lineup.Category{Name: "News", Base: 1000, Channels: 1}
	dry := appliedPlan(news)
	dry.DryRun = true
	for name, tc := range map[string]struct {
		lineup *clustarrv1.LineupSpec
		stored *livetvstore.LineupPlan
		status metav1.ConditionStatus
		reason string
		msg    string
	}{
		"no plan stored": {&clustarrv1.LineupSpec{Enabled: true}, nil, metav1.ConditionUnknown, "Pending", "not been planned"},
		"invalid": {
			&clustarrv1.LineupSpec{Enabled: true}, &livetvstore.LineupPlan{Error: "category News: names[0]: bad"},
			metav1.ConditionFalse, clustarrv1.ReasonInvalidLineup, "category News: names[0]: bad",
		},
		"a dry run":        {&clustarrv1.LineupSpec{}, dry, metav1.ConditionFalse, clustarrv1.ReasonDryRun, "enabled is false"},
		"enabled, not yet": {&clustarrv1.LineupSpec{Enabled: true}, dry, metav1.ConditionFalse, clustarrv1.ReasonDryRun, "has not applied"},
		"applied":          {&clustarrv1.LineupSpec{Enabled: true}, appliedPlanFor([]clustarrv1.IPTVChannel{catChannel("1001", "News")}, news), metav1.ConditionTrue, clustarrv1.ReasonApplied, "1 channels in 1 categories"},
		"applied, not on the channels yet": {
			&clustarrv1.LineupSpec{Enabled: true}, appliedPlanFor([]clustarrv1.IPTVChannel{catChannel("1007", "News")}, news),
			metav1.ConditionTrue, clustarrv1.ReasonApplied, "1 channels in 1 categories; not on the channels yet",
		},
		"disabled, applied": {&clustarrv1.LineupSpec{}, appliedPlan(news), metav1.ConditionFalse, clustarrv1.ReasonDryRun, "enabled is false"},
	} {
		t.Run(name, func(t *testing.T) {
			p := lineupProvider(false)
			p.Spec.Lineup = tc.lineup
			r := fakeReconciler(t, testAddress, p)
			r.PlanReader = storedPlan(tc.stored)
			pl, err := r.planBlocks(context.Background(), p, []clustarrv1.IPTVChannel{catChannel("1001", "News")})
			require.NoError(t, err)
			st := render(p, testAddress, pl, &snapTuner{snap: servedSnapshot(pl), ok: true}, validCondition, resolved{}, false, time.Now()).Status
			c := condition(st, clustarrv1.ConditionLineupApplied)
			require.NotNil(t, c)
			assert.Equal(t, tc.status, *c.Status)
			assert.Equal(t, tc.reason, *c.Reason)
			switch {
			case tc.reason == clustarrv1.ReasonInvalidLineup:
				assert.Equal(t, tc.msg, *c.Message, "the message is the error")
			case name == "applied":
				assert.Equal(t, tc.msg, *c.Message, "nothing pending")
			default:
				assert.Contains(t, *c.Message, tc.msg)
			}
			if tc.stored == nil {
				assert.Empty(t, st.Categories, "no plan, no categories")
			}
		})
	}
}

// TestAnInvalidLineupKeepsTheCategoriesItHas: a plan object holding only an
// error re-sends the categories the status has, as it holds the blocks.
func TestAnInvalidLineupKeepsTheCategoriesItHas(t *testing.T) {
	p := lineupProvider(true)
	dev := hdhr.BlockDeviceID("media", lineupProviderName, "cat/News")
	p.Status.Categories = []clustarrv1.CategoryStatus{{Name: "News", Base: 1000, Channels: 2, DeviceID: dev}}
	p.Status.Blocks = []clustarrv1.BlockStatus{{Start: "1001", First: "1001", Last: "1002", Category: "News", DeviceID: dev, GuidePath: BlockGuidePath}}
	r := fakeReconciler(t, testAddress, p)
	r.PlanReader = storedPlan(&livetvstore.LineupPlan{Error: "bad"})
	pl, err := r.planBlocks(context.Background(), p, []clustarrv1.IPTVChannel{catChannel("1001", "News"), catChannel("1002", "News")})
	require.NoError(t, err)
	st := render(p, testAddress, pl, &snapTuner{snap: servedSnapshot(pl), ok: true}, validCondition, resolved{}, false, time.Now()).Status
	require.Len(t, st.Categories, 1)
	assert.Equal(t, "News", *st.Categories[0].Name)
	assert.Equal(t, int32(2), *st.Categories[0].Channels)
	assert.Equal(t, dev, *st.Categories[0].DeviceID)
	require.Len(t, st.Blocks, 1)
	assert.Equal(t, "News", *st.Blocks[0].Category)
	assert.Equal(t, clustarrv1.ReasonInvalidLineup, *condition(st, clustarrv1.ConditionReady).Reason, "the last good blocks are kept")
}

// TestGuideSourcesReadyFollowsTheSecretAndTheGridLoop: GuideSourcesReady and
// status.guideSources.plex only while spec.guideSources.plex is set: the
// token's Secret first, then the grid loop's hold, its last error, and its
// first pass.
func TestGuideSourcesReadyFollowsTheSecretAndTheGridLoop(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	fetched := tuner.GuideSourcesState{Lineups: 3, Stations: 180, Grids: 175, FetchedAt: now.Add(-time.Hour)}
	for name, tc := range map[string]struct {
		res    resolved
		gs     tuner.GuideSourcesState
		status metav1.ConditionStatus
		reason string
	}{
		"no Secret":         {resolved{guideTokenReason: clustarrv1.ReasonSecretNotFound, guideTokenError: "the Plex token Secret media/plex-token does not exist"}, fetched, metav1.ConditionFalse, clustarrv1.ReasonSecretNotFound},
		"no key":            {resolved{guideTokenReason: clustarrv1.ReasonSecretKeyNotFound, guideTokenError: "no key"}, fetched, metav1.ConditionFalse, clustarrv1.ReasonSecretKeyNotFound},
		"held":              {resolved{}, tuner.GuideSourcesState{Lineups: 3, FetchedAt: now.Add(-time.Hour), HoldUntil: now.Add(2 * time.Minute), Error: "429"}, metav1.ConditionFalse, "RateLimited"},
		"a hold that ended": {resolved{}, tuner.GuideSourcesState{Lineups: 3, FetchedAt: now.Add(-time.Hour), HoldUntil: now.Add(-time.Minute)}, metav1.ConditionTrue, "Fetched"},
		"failed":            {resolved{}, tuner.GuideSourcesState{Lineups: 3, Error: "epg.provider.plex.tv: 500"}, metav1.ConditionFalse, "FetchFailed"},
		"fetched":           {resolved{}, fetched, metav1.ConditionTrue, "Fetched"},
		"not yet":           {resolved{}, tuner.GuideSourcesState{Lineups: 3}, metav1.ConditionFalse, "Pending"},
	} {
		t.Run(name, func(t *testing.T) {
			p := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "argon"}}
			p.Spec.GuideSources = &clustarrv1.GuideSources{Plex: &clustarrv1.PlexGuideSource{Lineups: []string{plexLineupID}}}
			tn := &snapTuner{snap: tuner.Snapshot{PlaylistFetched: true, GuideSources: tc.gs}, ok: true}
			st := render(p, testAddress, plan{}, tn, validCondition, tc.res, false, now).Status
			c := condition(st, clustarrv1.ConditionGuideSourcesReady)
			require.NotNil(t, c)
			assert.Equal(t, tc.status, *c.Status, *c.Message)
			assert.Equal(t, tc.reason, *c.Reason)
			require.NotNil(t, st.GuideSources)
			require.NotNil(t, st.GuideSources.Plex)
			ps := st.GuideSources.Plex
			// The counts are not omitempty: always sent, zero or not.
			require.NotNil(t, ps.Lineups)
			require.NotNil(t, ps.Stations)
			require.NotNil(t, ps.Grids)
			assert.Equal(t, int32(tc.gs.Lineups), *ps.Lineups)
			assert.Equal(t, int32(tc.gs.Stations), *ps.Stations)
			assert.Equal(t, int32(tc.gs.Grids), *ps.Grids)
			if tc.gs.HoldUntil.IsZero() {
				assert.Nil(t, ps.HoldUntil)
			} else {
				assert.Equal(t, metav1.NewTime(tc.gs.HoldUntil), *ps.HoldUntil)
			}
			if tc.gs.Error != "" {
				assert.Equal(t, tc.gs.Error, *ps.Error)
			}
		})
	}
}

// TestATunerThatDoesNotAnswerKeepsTheGuideSourcesStatus: before the tuner
// reports, status.guideSources.plex and GuideSourcesReady are re-sent as the
// status has them.
func TestATunerThatDoesNotAnswerKeepsTheGuideSourcesStatus(t *testing.T) {
	fetchedAt := metav1.NewTime(time.Date(2026, 11, 10, 11, 0, 0, 0, time.UTC))
	p := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "argon"}}
	p.Spec.GuideSources = &clustarrv1.GuideSources{Plex: &clustarrv1.PlexGuideSource{}}
	p.Status.GuideSources = &clustarrv1.GuideSourcesStatus{Plex: &clustarrv1.PlexSourceStatus{Lineups: 3, Stations: 180, Grids: 0, FetchedAt: &fetchedAt}}
	p.Status.Conditions = []metav1.Condition{{Type: clustarrv1.ConditionGuideSourcesReady, Status: metav1.ConditionTrue, Reason: "Fetched", Message: "kept", LastTransitionTime: fetchedAt}}
	st := render(p, testAddress, plan{}, &snapTuner{ok: false}, validCondition, resolved{}, false, time.Now()).Status
	require.NotNil(t, st.GuideSources)
	require.NotNil(t, st.GuideSources.Plex)
	assert.Equal(t, int32(180), *st.GuideSources.Plex.Stations)
	assert.Equal(t, int32(0), *st.GuideSources.Plex.Grids, "a zero count is sent")
	assert.Equal(t, fetchedAt, *st.GuideSources.Plex.FetchedAt)
	c := condition(st, clustarrv1.ConditionGuideSourcesReady)
	require.NotNil(t, c)
	assert.Equal(t, "kept", *c.Message)
}

// TestAProviderWithoutTheNewBlocksHasNoNewStatus: no spec.lineup and no
// spec.guideSources is no LineupApplied, no GuideSourcesReady, no
// categories and no guideSources status, and conditions the status still
// carries from them are released.
func TestAProviderWithoutTheNewBlocksHasNoNewStatus(t *testing.T) {
	p := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "argon"}}
	p.Status.Conditions = []metav1.Condition{
		{Type: clustarrv1.ConditionLineupApplied, Status: metav1.ConditionTrue, Reason: clustarrv1.ReasonApplied},
		{Type: clustarrv1.ConditionGuideSourcesReady, Status: metav1.ConditionTrue, Reason: "Fetched"},
	}
	p.Status.Categories = []clustarrv1.CategoryStatus{{Name: "News", Base: 1000}}
	p.Status.GuideSources = &clustarrv1.GuideSourcesStatus{Plex: &clustarrv1.PlexSourceStatus{Lineups: 1}}
	r := fakeReconciler(t, testAddress, p)
	r.PlanReader = storedPlan(appliedPlan(lineup.Category{Name: "News", Base: 1000}))
	pl, err := r.planBlocks(context.Background(), p, activeChannels("1001"))
	require.NoError(t, err)
	for _, ok := range []bool{true, false} {
		st := render(p, testAddress, pl, &snapTuner{snap: servedSnapshot(pl), ok: ok}, validCondition, resolved{}, false, time.Now()).Status
		assert.Nil(t, condition(st, clustarrv1.ConditionLineupApplied))
		assert.Nil(t, condition(st, clustarrv1.ConditionGuideSourcesReady))
		assert.Empty(t, st.Categories)
		assert.Nil(t, st.GuideSources)
		assert.Len(t, st.Conditions, 4, "Valid, PlaylistFetched, GuidesFetched, Ready")
	}
}
