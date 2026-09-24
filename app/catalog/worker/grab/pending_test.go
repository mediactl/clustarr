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

package grab

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// hdBlurayWebProfile is the real hd-bluray-web built-in (spec §9), not a
// hand-made ladder: keep-best has to agree with the profile the rest of
// Clustarr ranks against, and a fake two-value ladder would pass while
// disagreeing with UpgradeDecision's actual tier and revision handling.
func hdBlurayWebProfile(t *testing.T) quality.Profile {
	t.Helper()
	profiles, errs := quality.BuiltinProfiles(catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	p, ok := profiles["hd-bluray-web"]
	require.True(t, ok, "hd-bluray-web built-in profile is missing")
	require.GreaterOrEqual(t, len(p.Tiers), 3)
	return p
}

func topQuality(p quality.Profile) commonv1.Quality { return p.Tiers[0][0].Quality }

func bottomQuality(p quality.Profile) commonv1.Quality { return p.Tiers[len(p.Tiers)-1][0].Quality }

// newTestBus brings up the WHOLE default topology, single-node, rather than
// the one bucket a test needs: events.Topology.Validate requires the DLQ
// stream, and hand-rolling a partial topology both fails that check and drifts
// from the bucket TTLs and consumer tuning production actually applies.
func newTestBus(t *testing.T) events.Bus {
	t.Helper()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

func newPendingKV(t *testing.T) events.KV {
	t.Helper()
	return newTestBus(t).KV(events.BucketPending)
}

const pendingTestKey = "grab.movie-media-the-thing-1982-0123456789"

func TestCasKeepBest_FirstCandidateCreatesAndStampsFirstSeen(t *testing.T) {
	ctx := context.Background()
	kv := newPendingKV(t)
	profile := hdBlurayWebProfile(t)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	candidate := pendingValue{
		Target:  commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-thing-1982"},
		Release: commonv1.ReleaseInfo{GUID: "guid-1", Quality: bottomQuality(profile), FormatScore: 10},
	}
	kept, err := casKeepBest(ctx, kv, pendingTestKey, profile, candidate, now)
	require.NoError(t, err)
	assert.True(t, kept.FirstSeen.Equal(now), "FirstSeen = %v, want %v", kept.FirstSeen, now)
	assert.Equal(t, "guid-1", kept.Release.GUID)
}

func TestCasKeepBest_BetterCandidateReplacesButKeepsFirstSeen(t *testing.T) {
	ctx := context.Background()
	kv := newPendingKV(t)
	profile := hdBlurayWebProfile(t)
	firstSeen := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	worse := pendingValue{Release: commonv1.ReleaseInfo{GUID: "guid-worse", Quality: bottomQuality(profile)}}
	_, err := casKeepBest(ctx, kv, pendingTestKey, profile, worse, firstSeen)
	require.NoError(t, err)

	better := pendingValue{Release: commonv1.ReleaseInfo{GUID: "guid-better", Quality: topQuality(profile), FormatScore: 50}}
	kept, err := casKeepBest(ctx, kv, pendingTestKey, profile, better, firstSeen.Add(10*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, "guid-better", kept.Release.GUID)
	assert.True(t, kept.FirstSeen.Equal(firstSeen),
		"FirstSeen = %v, want the original %v: the delay clock must not reset", kept.FirstSeen, firstSeen)
}

func TestCasKeepBest_WorseCandidateIsIgnored(t *testing.T) {
	ctx := context.Background()
	kv := newPendingKV(t)
	profile := hdBlurayWebProfile(t)
	firstSeen := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	best := pendingValue{Release: commonv1.ReleaseInfo{GUID: "guid-best", Quality: topQuality(profile), FormatScore: 50}}
	_, err := casKeepBest(ctx, kv, pendingTestKey, profile, best, firstSeen)
	require.NoError(t, err)

	worse := pendingValue{Release: commonv1.ReleaseInfo{GUID: "guid-worse", Quality: bottomQuality(profile)}}
	kept, err := casKeepBest(ctx, kv, pendingTestKey, profile, worse, firstSeen.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, "guid-best", kept.Release.GUID)
	assert.True(t, kept.FirstSeen.Equal(firstSeen))
}

// TestCasKeepBest_KeysAndGrabbedByRoundTrip proves the pack narrowing and the
// grab provenance survive the JSON round trip through the bucket -- Handle
// reads them back out of the entry, not out of the task.
func TestCasKeepBest_KeysAndGrabbedByRoundTrip(t *testing.T) {
	ctx := context.Background()
	kv := newPendingKV(t)
	profile := hdBlurayWebProfile(t)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	candidate := pendingValue{
		Target:    commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "the-wire"},
		Keys:      []string{"the-wire-s01e01", "the-wire-s01e02"},
		Release:   commonv1.ReleaseInfo{GUID: "pack", Quality: bottomQuality(profile)},
		GrabbedBy: "rss",
	}
	_, err := casKeepBest(ctx, kv, pendingTestKey, profile, candidate, now)
	require.NoError(t, err)

	// A worse challenger returns the stored incumbent, decoded from the
	// bucket rather than echoed back from the argument.
	worse := pendingValue{Release: commonv1.ReleaseInfo{GUID: "worse"}}
	kept, err := casKeepBest(ctx, kv, pendingTestKey, profile, worse, now.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, []string{"the-wire-s01e01", "the-wire-s01e02"}, kept.Keys)
	assert.Equal(t, commonv1.MediaKindSeries, kept.Target.Kind)
	assert.EqualValues(t, "rss", kept.GrabbedBy)
}

// mediaRefEpisode is the shorthand the lease tests share.
func mediaRefEpisode(name string) commonv1.MediaRef {
	return commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: name}
}
