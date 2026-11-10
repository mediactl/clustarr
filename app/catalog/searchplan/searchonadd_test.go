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

package searchplan_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	"github.com/mediactl/clustarr/pkg/events"
)

// TestAnItemAddedUnsearchedAsksTheIndexFirst is the 2026-11-10 design's
// searchForMissing (D10, D11; Sonarr's "search for missing episodes"
// unticked): an item added with the flag off is View.NoSearchOnAdd, and
// its first automatic search asks the release index alone -- never paced,
// on the low lane -- where any other item's first search is live. A
// backlog grant and a redownload are still live for it, it does not wait
// for its season's automatic pack search (which SeasonFacts.FirstSearch
// leaves it out of), and once searched the flag changes nothing.
func TestAnItemAddedUnsearchedAsksTheIndexFirst(t *testing.T) {
	unpaced := func(t *testing.T) func() time.Time {
		return func() time.Time {
			t.Helper()
			t.Fatal("an index-only search never reserves a pace slot")
			return t0
		}
	}
	for _, tc := range []struct {
		name  string
		view  func(v *searchplan.View)
		live  bool
		waits bool
	}{
		{"its first search is live by default", func(*searchplan.View) {}, true, false},
		{"added unsearched, its first search asks the index", func(v *searchplan.View) { v.NoSearchOnAdd = true }, false, false},
		{"added unsearched, it waits for no pack-first season", func(v *searchplan.View) { v.NoSearchOnAdd, v.SeasonPackFirst = true, true }, false, false},
		{"search on add, it waits for its pack-first season", func(v *searchplan.View) { v.SeasonPackFirst = true }, false, true},
		{"a pending season search is waited for either way", func(v *searchplan.View) { v.NoSearchOnAdd, v.SeasonPending = true, true }, false, true},
		{"a backlog grant is live for it", func(v *searchplan.View) { v.NoSearchOnAdd, v.Backlog = true, true }, true, false},
		{"a redownload is live for it", func(v *searchplan.View) {
			v.NoSearchOnAdd = true
			failed := t0.Add(-time.Minute)
			v.Redownload = &failed
		}, true, false},
		{"once searched the flag changes nothing", func(v *searchplan.View) {
			v.NoSearchOnAdd = true
			v.Attempts = commonv1.Attempts{Count: 1, Initial: ago(7 * time.Hour), Latest: ago(7 * time.Hour)}
			v.LastSearched = v.Attempts.Latest
		}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := itemView("ep", t0)
			tc.view(&v)
			pace := unpaced(t)
			if tc.live {
				pace = func() time.Time { return t0 }
			}
			p := searchplan.Decide(v, admitAll, pace)
			if tc.waits {
				require.Nil(t, p.Publish, "it waits for its season")
				return
			}
			require.NotNil(t, p.Publish)
			require.Equal(t, tc.live, p.Publish.Live)
			require.Equal(t, !tc.live, p.Publish.Payload.IndexOnly)
			lane := events.PriorityLow
			if tc.live {
				lane = events.PriorityNormal
			}
			require.Equal(t, events.WorkSearchSubject(lane, events.MediaKey("episode", ns, "ep")), p.Publish.Subject)
		})
	}
}
