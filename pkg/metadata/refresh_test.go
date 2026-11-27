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

package metadata_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
)

func TestRefreshTTLPortsTheArrHeuristics(t *testing.T) {
	fresh := time.Now()
	tests := []struct {
		name  string
		kind  commonv1.MediaKind
		state string
		want  time.Duration
	}{
		{"movie announced", commonv1.MediaKindMovie, metadata.RefreshStateAnnounced, 12 * time.Hour},
		{"movie in cinemas", commonv1.MediaKindMovie, metadata.RefreshStateInCinemas, 12 * time.Hour},
		{"movie released recently", commonv1.MediaKindMovie, metadata.RefreshStateReleasedRecent, 24 * time.Hour},
		{"movie released long ago", commonv1.MediaKindMovie, metadata.RefreshStateReleasedOld, 7 * 24 * time.Hour},
		{"series continuing", commonv1.MediaKindSeries, metadata.RefreshStateContinuing, 6 * time.Hour},
		{"series ended recently", commonv1.MediaKindSeries, metadata.RefreshStateEndedRecent, 24 * time.Hour},
		{"series ended long ago", commonv1.MediaKindSeries, metadata.RefreshStateEndedOld, 30 * 24 * time.Hour},
		{"album", commonv1.MediaKindAlbum, metadata.RefreshStateActive, 7 * 24 * time.Hour},
		{"book", commonv1.MediaKindBook, metadata.RefreshStateActive, 30 * 24 * time.Hour},
		{"comic ongoing", commonv1.MediaKindComic, metadata.RefreshStateOngoing, 24 * time.Hour},
		{"comic completed", commonv1.MediaKindComic, metadata.RefreshStateCompleted, 30 * 24 * time.Hour},
		{"search results", commonv1.MediaKindMovie, metadata.RefreshStateSearch, time.Hour},
		{"id crosswalk", commonv1.MediaKindMovie, metadata.RefreshStateCrosswalk, 90 * 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, metadata.RefreshTTL(tt.kind, tt.state, fresh))
		})
	}
}

func TestRefreshTTLForcesAnImmediateRefreshPastTheHardCap(t *testing.T) {
	stale := time.Now().Add(-200 * 24 * time.Hour) // past the 180-day hard cap

	got := metadata.RefreshTTL(commonv1.MediaKindMovie, metadata.RefreshStateReleasedOld, stale)

	require.Zero(t, got, "a record untouched for 200 days must refresh immediately regardless of its bucket")
}

// A document is looked at again once its ttl has run out, no more than a
// sixteenth of the ttl late, and two items refreshed together are not due
// together.
func TestRefreshWakeLandsJustAfterTheDocumentIsDue(t *testing.T) {
	refreshed := time.Date(2026, 11, 17, 5, 47, 0, 0, time.UTC)
	now := refreshed.Add(2 * time.Hour)
	ttl := 6 * time.Hour

	a := metadata.RefreshWake(ttl, refreshed, now, "uid-a")
	b := metadata.RefreshWake(ttl, refreshed, now, "uid-b")

	for _, d := range []time.Duration{a, b} {
		require.GreaterOrEqual(t, d, 4*time.Hour, "never before the document is due")
		require.Less(t, d, 4*time.Hour+ttl/16)
	}
	require.NotEqual(t, a, b, "two items refreshed together drift apart")
	require.Equal(t, a, metadata.RefreshWake(ttl, refreshed, now, "uid-a"), "the same item wakes at the same time")
}

// The spread fills its sixteenth of the ttl: a library's items, refreshed
// together, come due across the whole of it. Until 2026-11-26 the 32-bit
// hash was taken as nanoseconds, so no two items were ever more than 4.3 s
// apart and a library came due at once (TestRefreshWake above passed: its
// two wakes differed, by nanoseconds).
func TestRefreshSpreadFillsASixteenthOfTheTTL(t *testing.T) {
	ttl := 7 * 24 * time.Hour
	lo, hi := ttl, time.Duration(0)
	for i := range 1000 {
		d := metadata.RefreshSpread(ttl, fmt.Sprintf("3f0c1a52-9f6e-4c9b-8a57-%012d", i))
		require.GreaterOrEqual(t, d, time.Duration(0))
		require.Less(t, d, ttl/16)
		lo, hi = min(lo, d), max(hi, d)
	}
	require.Less(t, lo, ttl/160, "some come due at the start of the spread")
	require.Greater(t, hi, ttl/16-ttl/160, "and some at its end")
	require.Zero(t, metadata.RefreshSpread(0, "uid"), "no ttl, no spread")
}

// An overdue document is looked at at once, never at a negative delay,
// which a requeue would read as "never".
func TestRefreshWakeIsNeverLessThanASecond(t *testing.T) {
	refreshed := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	require.Equal(t, time.Second, metadata.RefreshWake(6*time.Hour, refreshed, refreshed.Add(48*time.Hour), "uid"))
	require.Equal(t, time.Second, metadata.RefreshWake(0, refreshed, refreshed, "uid"))
}

// Each kind's document has a version of its own (owner, 2026-11-27: "Can we
// split this for each type?"): a Series that learned its TMDB ratings is
// refreshed once and no Movie, Album or Book is. No kind is ever below the
// version they shared, or every item of it would read as newer than the
// gateway and one written today as outdated.
func TestSchemaVersionIsEachKindsOwn(t *testing.T) {
	const shared = 9
	require.EqualValues(t, 10, metadata.SchemaVersion(commonv1.MediaKindSeries))
	for _, kind := range []commonv1.MediaKind{
		commonv1.MediaKindMovie, commonv1.MediaKindArtist, commonv1.MediaKindAlbum, commonv1.MediaKindAuthor,
		commonv1.MediaKindBook, commonv1.MediaKindAudiobook, commonv1.MediaKindComic,
	} {
		require.EqualValues(t, shared, metadata.SchemaVersion(kind), "%s has learned nothing since the kinds parted", kind)
	}
	require.EqualValues(t, shared, metadata.SchemaVersion("albumlist"), "a cache key that is no item's kind keeps the shared version")
	for _, kind := range []commonv1.MediaKind{
		commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindArtist, commonv1.MediaKindAlbum,
		commonv1.MediaKindAuthor, commonv1.MediaKindBook, commonv1.MediaKindAudiobook, commonv1.MediaKindComic,
	} {
		require.GreaterOrEqual(t, metadata.SchemaVersion(kind), int32(shared), "%s", kind)
	}
	require.Equal(t, "metadata-schema10", metadata.RefreshPurpose(commonv1.MediaKindSeries, true))
	require.Equal(t, "metadata-schema9", metadata.RefreshPurpose(commonv1.MediaKindAlbum, true))
	require.Equal(t, "metadata", metadata.RefreshPurpose(commonv1.MediaKindSeries, false))
}
