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
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/pkg/iptv/blocks"
	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
	"github.com/mediactl/clustarr/pkg/iptv/lineup"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
)

const testAddress = "clustarr-livetv.media.svc.cluster.local:80"

func fakeReconciler(t *testing.T, address string, objs ...*clustarrv1.IPTVProvider) *Reconciler {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clustarrv1.AddToScheme(s))
	b := fake.NewClientBuilder().WithScheme(s)
	for _, o := range objs {
		b = b.WithObjects(o)
	}
	return &Reconciler{Client: b.Build(), Address: address}
}

func activeChannels(numbers ...string) []clustarrv1.IPTVChannel {
	out := make([]clustarrv1.IPTVChannel, 0, len(numbers))
	for _, n := range numbers {
		out = append(out, clustarrv1.IPTVChannel{Spec: clustarrv1.IPTVChannelSpec{Number: n, Active: true}})
	}
	return out
}

// TestPlanBlocksGivesEachBlockItsURI: a block's device is /hdhr/<deviceID>
// on the livetv Service, and its URI is that path on the Service's
// address -- what Plex is given.
func TestPlanBlocksGivesEachBlockItsURI(t *testing.T) {
	p := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "news"}}
	max := int32(2)
	p.Spec.DVR.MaxChannels = &max
	r := fakeReconciler(t, testAddress, p)
	pl, err := r.planBlocks(context.Background(), p, activeChannels("1001", "1002", "1003", "1004"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(pl.blocks), 2, "two channels a block")
	for _, b := range pl.blocks {
		assert.Equal(t, "/hdhr/"+b.DeviceID, b.Path, b.Start)
		assert.Equal(t, "http://"+testAddress+"/hdhr/"+b.DeviceID, b.URI, b.Start)
	}

	r = fakeReconciler(t, "", p)
	pl, err = r.planBlocks(context.Background(), p, activeChannels("1001"))
	require.NoError(t, err)
	require.Len(t, pl.blocks, 1)
	assert.Equal(t, "/hdhr/"+pl.blocks[0].DeviceID, pl.blocks[0].Path)
	assert.Empty(t, pl.blocks[0].URI, "no address, no URI: cluster-plex registers nothing")
}

// TestKeptBlocksCarryTheirURITheSameWay: blocks kept because a rule refused
// the next ones (here another, older provider's span) still name their
// path and URI, so blocks a previous manager wrote get them too.
func TestKeptBlocksCarryTheirURITheSameWay(t *testing.T) {
	older := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "sport", CreationTimestamp: metav1.NewTime(time.Unix(100, 0))}}
	older.Status.Blocks = []clustarrv1.BlockStatus{{Start: "1001", First: "1001", Last: "1002", DeviceID: "AAAA0001"}}
	p := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "news", CreationTimestamp: metav1.NewTime(time.Unix(200, 0))}}
	p.Status.Blocks = []clustarrv1.BlockStatus{{Start: "2001", First: "2001", Last: "2001", DeviceID: "BBBB0001"}}
	r := fakeReconciler(t, testAddress, older, p)
	pl, err := r.planBlocks(context.Background(), p, activeChannels("1001", "2001"))
	require.NoError(t, err)
	require.Equal(t, clustarrv1.ReasonNumberRangeOverlap, pl.reason)
	require.Len(t, pl.blocks, 1)
	assert.Equal(t, "/hdhr/BBBB0001", pl.blocks[0].Path)
	assert.Equal(t, "http://"+testAddress+"/hdhr/BBBB0001", pl.blocks[0].URI)
}

const lineupProviderName = "argon"

// lineupProvider is a provider with spec.lineup, enabled or a dry run.
func lineupProvider(enabled bool) *clustarrv1.IPTVProvider {
	p := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: lineupProviderName}}
	p.Spec.Lineup = &clustarrv1.LineupSpec{Enabled: enabled}
	return p
}

// catChannel is an active channel the lineup planner put in category ("" is
// none).
func catChannel(number, category string) clustarrv1.IPTVChannel {
	ch := clustarrv1.IPTVChannel{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "ch-" + number}}
	ch.Spec = clustarrv1.IPTVChannelSpec{ProviderRef: lineupProviderName, Key: "t:ch" + number, Number: number, Active: true}
	if category != "" {
		ch.Status.Lineup = &clustarrv1.ChannelLineupStatus{Category: category, Number: number}
	}
	return ch
}

// storedPlan answers lp as the provider's stored lineup plan; nil is none
// stored.
func storedPlan(lp *livetvstore.LineupPlan) func(context.Context, types.NamespacedName) (*livetvstore.LineupPlan, bool, error) {
	return func(context.Context, types.NamespacedName) (*livetvstore.LineupPlan, bool, error) {
		if lp == nil {
			return nil, false, nil
		}
		return lp, true, nil
	}
}

func appliedPlan(cats ...lineup.Category) *livetvstore.LineupPlan {
	return &livetvstore.LineupPlan{PlannedAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC), Plan: lineup.Plan{Categories: cats}}
}

// appliedPlanFor is an applied plan the channels already carry: each active
// channel of a listed category not refused as TooMany is assigned its
// number.
func appliedPlanFor(chans []clustarrv1.IPTVChannel, cats ...lineup.Category) *livetvstore.LineupPlan {
	lp := appliedPlan(cats...)
	blocked := map[string]bool{}
	for _, c := range cats {
		blocked[c.Name] = !c.TooMany
	}
	for _, ch := range chans {
		if cat := categoryOf(&ch); ch.Spec.Active && blocked[cat] {
			lp.Channels = append(lp.Channels, lineup.Assignment{Key: ch.Spec.Key, Category: cat, Number: ch.Spec.Number})
		}
	}
	return lp
}

// TestCategoriesAreBlocksOfTheirOwn: with spec.lineup enabled and a plan
// stored, each category holding an active channel is one block, starting at
// its base+1, under a device of its own named for the category; a channel
// in no category is on no block, and a category with no active channel has
// none.
func TestCategoriesAreBlocksOfTheirOwn(t *testing.T) {
	p := lineupProvider(true)
	inactive := catChannel("1004", "News")
	inactive.Spec.Active = false
	chans := []clustarrv1.IPTVChannel{
		catChannel("2002", "Sports"), catChannel("1003", "News"), catChannel("1001", "News"),
		catChannel("2001", "Sports"), catChannel("1002", "News"), catChannel("1500", ""), inactive,
	}
	r := fakeReconciler(t, testAddress, p)
	// The plan lists its categories in rule order, not by base.
	r.PlanReader = storedPlan(appliedPlanFor(chans,
		lineup.Category{Name: "Sports", Base: 2000, Channels: 2},
		lineup.Category{Name: "News", Base: 1000, Channels: 3},
		lineup.Category{Name: "Movies", Base: 4000},
	))
	pl, err := r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	require.Empty(t, pl.reason, pl.message)
	require.Len(t, pl.blocks, 2, "News and Sports; Movies holds no active channel")

	news, sports := pl.blocks[0], pl.blocks[1]
	assert.Equal(t, "1001", news.Start, "ordered by start")
	assert.Equal(t, "News", news.Category)
	assert.Equal(t, hdhr.BlockDeviceID("media", lineupProviderName, "cat/News"), news.DeviceID)
	assert.Equal(t, "Clustarr "+lineupProviderName+" News", news.FriendlyName)
	assert.Equal(t, BlockGuidePath, news.GuidePath)
	assert.Empty(t, news.Lineup)
	assert.Equal(t, "/hdhr/"+news.DeviceID, news.Path)
	assert.Equal(t, "http://"+testAddress+"/hdhr/"+news.DeviceID, news.URI)
	assert.Equal(t, int32(3), news.Channels)
	assert.Equal(t, "1001", news.First)
	assert.Equal(t, "1003", news.Last)
	assert.Equal(t, int32(blocks.MapBytes([]string{"1001", "1002", "1003"})), news.MapBytes)
	assert.Equal(t, []string{"1001", "1002", "1003"}, pl.numbers["1001"], "sorted; the inactive channel is in no block")

	assert.Equal(t, "2001", sports.Start)
	assert.Equal(t, "Sports", sports.Category)
	assert.Equal(t, hdhr.BlockDeviceID("media", lineupProviderName, "cat/Sports"), sports.DeviceID)
	assert.Equal(t, "Clustarr "+lineupProviderName+" Sports", sports.FriendlyName)
	assert.Equal(t, []string{"2001", "2002"}, pl.numbers["2001"])

	for start, numbers := range pl.numbers {
		assert.NotContains(t, numbers, "1500", "a channel in no category is on no block (%s)", start)
	}
	cfg := tunerBlocks(pl)
	require.Len(t, cfg, 2)
	assert.Equal(t, "News", cfg[0].Category, "the tuner learns each block's category")
	assert.Equal(t, "Sports", cfg[1].Category)
	assert.Equal(t, []string{"2001", "2002"}, cfg[1].Numbers)
}

// TestACategoryBlockKeepsItsDeviceIDAcrossARenumbering: a category's device
// is its identity, so a new base (a new start) or new numbers keep the
// device the status names for the category, and with it its Plex DVR.
func TestACategoryBlockKeepsItsDeviceIDAcrossARenumbering(t *testing.T) {
	p := lineupProvider(true)
	r := fakeReconciler(t, testAddress, p)
	chans := []clustarrv1.IPTVChannel{catChannel("1001", "News"), catChannel("1002", "News")}
	r.PlanReader = storedPlan(appliedPlanFor(chans, lineup.Category{Name: "News", Base: 1000, Channels: 2}))
	pl, err := r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	require.Len(t, pl.blocks, 1)
	fresh := pl.blocks[0].DeviceID
	assert.Equal(t, hdhr.BlockDeviceID("media", lineupProviderName, "cat/News"), fresh)

	// A third channel joins, and the numbers move: the same device.
	p.Status.Blocks = pl.blocks
	chans = append(chans, catChannel("1003", "News"))
	r.PlanReader = storedPlan(appliedPlanFor(chans, lineup.Category{Name: "News", Base: 1000, Channels: 3}))
	pl, err = r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	require.Len(t, pl.blocks, 1)
	assert.Equal(t, fresh, pl.blocks[0].DeviceID)
	assert.Equal(t, "1003", pl.blocks[0].Last)

	// The status's device for the category is kept even when it is not the
	// hash, and across a new base.
	p.Status.Blocks = []clustarrv1.BlockStatus{{Start: "1001", First: "1001", Last: "1003", Category: "News", DeviceID: "0A0B0C0D", GuidePath: BlockGuidePath}}
	chans = []clustarrv1.IPTVChannel{catChannel("1101", "News"), catChannel("1102", "News")}
	r.PlanReader = storedPlan(appliedPlanFor(chans, lineup.Category{Name: "News", Base: 1100, Channels: 2}))
	pl, err = r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	require.Len(t, pl.blocks, 1)
	assert.Equal(t, "1101", pl.blocks[0].Start, "base+1")
	assert.Equal(t, "0A0B0C0D", pl.blocks[0].DeviceID)
	assert.Equal(t, []string{"1101", "1102"}, pl.numbers["1101"])
}

// TestLineupOffPacksAsBefore: a provider without spec.lineup, with a dry
// run, with no plan stored yet, or with a plan the lineup controller has not
// applied yet, packs its blocks as before, whatever its channels' status
// says; and blocks of categories are no boundaries of those.
func TestLineupOffPacksAsBefore(t *testing.T) {
	chans := []clustarrv1.IPTVChannel{catChannel("1001", "News"), catChannel("1002", "News"), catChannel("2001", "Sports")}
	plain := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: lineupProviderName}}
	want, err := fakeReconciler(t, testAddress, plain).planBlocks(context.Background(), plain, chans)
	require.NoError(t, err)
	require.Len(t, want.blocks, 1)
	require.Equal(t, "1001", want.blocks[0].Start)
	require.Empty(t, want.blocks[0].Category)
	require.Equal(t, "Clustarr "+lineupProviderName+" 1001+", want.blocks[0].FriendlyName)

	applied := appliedPlan(lineup.Category{Name: "News", Base: 1000, Channels: 2}, lineup.Category{Name: "Sports", Base: 2000, Channels: 1})
	dry := appliedPlan(applied.Categories...)
	dry.DryRun = true
	for name, tc := range map[string]struct {
		lineup *clustarrv1.LineupSpec
		stored *livetvstore.LineupPlan
	}{
		"no spec.lineup, a plan stored": {nil, applied},
		"a dry run":                     {&clustarrv1.LineupSpec{}, dry},
		"a dry run, a plan applied":     {&clustarrv1.LineupSpec{}, applied},
		"enabled, no plan stored":       {&clustarrv1.LineupSpec{Enabled: true}, nil},
		"enabled, the dry run's plan":   {&clustarrv1.LineupSpec{Enabled: true}, dry},
	} {
		t.Run(name, func(t *testing.T) {
			p := plain.DeepCopy()
			p.Spec.Lineup = tc.lineup
			r := fakeReconciler(t, testAddress, p)
			r.PlanReader = storedPlan(tc.stored)
			pl, err := r.planBlocks(context.Background(), p, chans)
			require.NoError(t, err)
			assert.Empty(t, pl.reason)
			assert.Equal(t, want.blocks, pl.blocks)
			assert.Equal(t, want.numbers, pl.numbers)
		})
	}

	// Turned off after the categories were published: packed afresh, the
	// categories' starts and devices no boundary or identity of the blocks.
	p := plain.DeepCopy()
	p.Spec.Lineup = &clustarrv1.LineupSpec{}
	p.Status.Blocks = []clustarrv1.BlockStatus{
		{Start: "1001", First: "1001", Last: "1002", Category: "News", DeviceID: hdhr.BlockDeviceID("media", lineupProviderName, "cat/News"), GuidePath: BlockGuidePath},
		{Start: "2001", First: "2001", Last: "2001", Category: "Sports", DeviceID: hdhr.BlockDeviceID("media", lineupProviderName, "cat/Sports"), GuidePath: BlockGuidePath},
	}
	r := fakeReconciler(t, testAddress, p)
	r.PlanReader = storedPlan(dry)
	pl, err := r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	assert.Equal(t, want.blocks, pl.blocks)
}

// TestAnInvalidLineupHoldsThePublishedBlocks: a stored plan carrying an
// error (a rule that does not compile) moves no block: the published ones,
// categories included, stand, each with its category's channels.
func TestAnInvalidLineupHoldsThePublishedBlocks(t *testing.T) {
	p := lineupProvider(true)
	newsDev := hdhr.BlockDeviceID("media", lineupProviderName, "cat/News")
	p.Status.Blocks = []clustarrv1.BlockStatus{
		{Start: "1001", First: "1001", Last: "1002", Category: "News", DeviceID: newsDev, GuidePath: BlockGuidePath},
		{Start: "2001", First: "2001", Last: "2001", Category: "Sports", DeviceID: "0A0B0C0D", GuidePath: BlockGuidePath},
	}
	r := fakeReconciler(t, testAddress, p)
	r.PlanReader = storedPlan(&livetvstore.LineupPlan{Error: "category News: names[0]: error parsing regexp: missing closing ]"})
	pl, err := r.planBlocks(context.Background(), p, []clustarrv1.IPTVChannel{catChannel("1001", "News"), catChannel("1002", "News"), catChannel("1003", "News"), catChannel("2001", "Sports")})
	require.NoError(t, err)
	assert.Equal(t, clustarrv1.ReasonInvalidLineup, pl.reason)
	assert.Contains(t, pl.message, "missing closing ]")
	require.Len(t, pl.blocks, 2)
	assert.Equal(t, newsDev, pl.blocks[0].DeviceID)
	assert.Equal(t, "http://"+testAddress+"/hdhr/"+newsDev, pl.blocks[0].URI)
	assert.Equal(t, []string{"1001", "1002", "1003"}, pl.numbers["1001"], "a held category block serves its category's channels")
	assert.Equal(t, []string{"2001"}, pl.numbers["2001"])
}

// TestCategoryBlocksKeepTheRulesAcrossProviders: the categories' span is
// held to another provider's published span as any packing is.
func TestCategoryBlocksKeepTheRulesAcrossProviders(t *testing.T) {
	older := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "sport", CreationTimestamp: metav1.NewTime(time.Unix(100, 0))}}
	older.Status.Blocks = []clustarrv1.BlockStatus{{Start: "2001", First: "2001", Last: "2100", DeviceID: "AAAA0001"}}
	p := lineupProvider(true)
	p.CreationTimestamp = metav1.NewTime(time.Unix(200, 0))
	r := fakeReconciler(t, testAddress, older, p)
	chans := []clustarrv1.IPTVChannel{catChannel("1001", "News"), catChannel("2001", "Sports")}
	r.PlanReader = storedPlan(appliedPlanFor(chans, lineup.Category{Name: "News", Base: 1000, Channels: 1}, lineup.Category{Name: "Sports", Base: 2000, Channels: 1}))
	pl, err := r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	assert.Equal(t, clustarrv1.ReasonNumberRangeOverlap, pl.reason)
	assert.Empty(t, pl.blocks, "nothing published, nothing kept")
}

// TestAPlanNotYetOnTheChannelsHoldsTheBlocks: the lineup controller stores
// its plan, then applies each channel's number and status in turn, so until
// every channel carries the plan the published blocks stand: blocks built
// from the dry run's statuses and the old numbers, or from a half-applied
// renumbering (one number in two categories), would each move a DVR's map.
// A plan that places no channel publishes no block.
func TestAPlanNotYetOnTheChannelsHoldsTheBlocks(t *testing.T) {
	published := []clustarrv1.BlockStatus{{Start: "1001", First: "1001", Last: "1257", DeviceID: "8246E54A", GuidePath: BlockGuidePath}}
	cats := []lineup.Category{{Name: "News", Base: 1000, Channels: 2}, {Name: "Sports", Base: 2000, Channels: 1}}
	// The plan: CNN 1001 and MSNBC 1002 in News, ESPN 2001 in Sports.
	planned := []clustarrv1.IPTVChannel{catChannel("1001", "News"), catChannel("1002", "News"), catChannel("2001", "Sports")}
	lp := appliedPlanFor(planned, cats...)

	// renumbered is the planned channels with spec numbers as given, the
	// status the dry run (or the apply) wrote.
	renumbered := func(numbers ...string) []clustarrv1.IPTVChannel {
		out := make([]clustarrv1.IPTVChannel, len(planned))
		for i := range planned {
			out[i] = *planned[i].DeepCopy()
			out[i].Spec.Number = numbers[i]
		}
		return out
	}
	for name, chans := range map[string][]clustarrv1.IPTVChannel{
		"the dry run's statuses, the old numbers": renumbered("1017", "1001", "1120"),
		// MSNBC is renumbered onto CNN's old number, CNN not yet: 1001 twice.
		"half applied":           renumbered("1017", "1001", "2001"),
		"no channel created yet": nil,
		"a channel not yet given its category": func() []clustarrv1.IPTVChannel {
			c := renumbered("1001", "1002", "2001")
			c[2].Status.Lineup = nil
			return c
		}(),
		"a dropped channel still active in a category": append(renumbered("1001", "1002", "2001"), catChannel("1003", "News")),
	} {
		t.Run(name, func(t *testing.T) {
			p := lineupProvider(true)
			p.Status.Blocks = published
			r := fakeReconciler(t, testAddress, p)
			r.PlanReader = storedPlan(lp)
			pl, err := r.planBlocks(context.Background(), p, chans)
			require.NoError(t, err)
			assert.Equal(t, clustarrv1.ReasonLineupPending, pl.reason)
			assert.Contains(t, pl.message, "do not carry their planned number")
			require.Len(t, pl.blocks, 1, "the published blocks are kept")
			assert.Equal(t, "8246E54A", pl.blocks[0].DeviceID)
		})
	}

	// Applied to every channel: the categories are published.
	p := lineupProvider(true)
	p.Status.Blocks = published
	r := fakeReconciler(t, testAddress, p)
	r.PlanReader = storedPlan(lp)
	pl, err := r.planBlocks(context.Background(), p, renumbered("1001", "1002", "2001"))
	require.NoError(t, err)
	assert.Empty(t, pl.reason, pl.message)
	require.Len(t, pl.blocks, 2)
	assert.Equal(t, "News", pl.blocks[0].Category)

	// A plan that places no channel publishes no block.
	r.PlanReader = storedPlan(appliedPlan(lineup.Category{Name: "News", Base: 1000}, lineup.Category{Name: "Movies", Base: 4000, Channels: 900, TooMany: true}))
	pl, err = r.planBlocks(context.Background(), p, activeChannels("1001", "1002"))
	require.NoError(t, err)
	assert.Empty(t, pl.reason)
	assert.Empty(t, pl.blocks)
}

// TestAVanishedPlanHoldsTheCategoryBlocks: with the lineup enabled and its
// categories published, a plan object gone from the store (a bucket
// re-created) or only a dry run's holds the blocks rather than pack by
// number, which would replace every category's DVR; before the first apply
// (no category block published) the packing by number goes on.
func TestAVanishedPlanHoldsTheCategoryBlocks(t *testing.T) {
	chans := []clustarrv1.IPTVChannel{catChannel("1001", "News"), catChannel("2001", "Sports")}
	newsDev := hdhr.BlockDeviceID("media", lineupProviderName, "cat/News")
	dry := appliedPlanFor(chans, lineup.Category{Name: "News", Base: 1000, Channels: 1}, lineup.Category{Name: "Sports", Base: 2000, Channels: 1})
	dry.DryRun = true
	for name, stored := range map[string]*livetvstore.LineupPlan{"none stored": nil, "a dry run's": dry} {
		t.Run(name, func(t *testing.T) {
			p := lineupProvider(true)
			p.Status.Blocks = []clustarrv1.BlockStatus{
				{Start: "1001", First: "1001", Last: "1001", Category: "News", DeviceID: newsDev, GuidePath: BlockGuidePath},
				{Start: "2001", First: "2001", Last: "2001", Category: "Sports", DeviceID: "0A0B0C0D", GuidePath: BlockGuidePath},
			}
			r := fakeReconciler(t, testAddress, p)
			r.PlanReader = storedPlan(stored)
			pl, err := r.planBlocks(context.Background(), p, chans)
			require.NoError(t, err)
			assert.Equal(t, clustarrv1.ReasonLineupPending, pl.reason)
			require.Len(t, pl.blocks, 2)
			assert.Equal(t, newsDev, pl.blocks[0].DeviceID)
			assert.Equal(t, "0A0B0C0D", pl.blocks[1].DeviceID)
			assert.Equal(t, []string{"2001"}, pl.numbers["2001"])

			// Before the first apply: packed by number.
			p.Status.Blocks = []clustarrv1.BlockStatus{{Start: "1001", First: "1001", Last: "2001", DeviceID: "8246E54A", GuidePath: BlockGuidePath}}
			pl, err = r.planBlocks(context.Background(), p, chans)
			require.NoError(t, err)
			assert.Empty(t, pl.reason)
			require.Len(t, pl.blocks, 1)
			assert.Equal(t, "8246E54A", pl.blocks[0].DeviceID)
			assert.Empty(t, pl.blocks[0].Category)
		})
	}
}

// TestABlocksFriendlyNameFitsTheStatus: spec.device.friendlyName may be 128
// runes and a category 64, and status.blocks[].friendlyName holds 160: the
// name is cut on a rune boundary, or the apiserver would refuse every status
// apply.
func TestABlocksFriendlyNameFitsTheStatus(t *testing.T) {
	friendly := strings.Repeat("é", 128)
	category := strings.Repeat("ü", 64)
	p := lineupProvider(true)
	p.Spec.Device.FriendlyName = friendly
	chans := []clustarrv1.IPTVChannel{catChannel("1001", category)}
	r := fakeReconciler(t, testAddress, p)
	r.PlanReader = storedPlan(appliedPlanFor(chans, lineup.Category{Name: category, Base: 1000, Channels: 1}))
	pl, err := r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	require.Len(t, pl.blocks, 1)
	name := pl.blocks[0].FriendlyName
	assert.Equal(t, 160, utf8.RuneCountInString(name))
	assert.True(t, utf8.ValidString(name), "cut on a rune boundary")
	assert.Equal(t, friendly+" "+strings.Repeat("ü", 31), name)

	// A name that fits is left whole.
	p.Spec.Device.FriendlyName = "Clustarr"
	pl, err = r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	assert.Equal(t, "Clustarr "+category, pl.blocks[0].FriendlyName)
}
