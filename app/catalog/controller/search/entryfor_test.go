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

package search

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// TestAPicksOwnBlocklistedGrabIsItsGrab is the smoke re-verification's
// finding (2026-10-08, e2e scenario 3): a pick of a release whose payload
// is gone is grabbed and Blocklisted within a second, before the Search
// looks again. Its entry is the pick's grab and must be reported; a
// Blocklisted tombstone grabbed before the Search existed is not (smoke T2:
// it would hide the release's refusal and keep an override pick from being
// sent).
func TestAPicksOwnBlocklistedGrabIsItsGrab(t *testing.T) {
	created := time.Date(2026, 10, 8, 6, 59, 44, 0, time.UTC)
	s := &catalogv1alpha1.Search{ObjectMeta: metav1.ObjectMeta{Name: "pick", CreationTimestamp: metav1.NewTime(created)}}
	entry := func(id, guid string, phase commonv1.DownloadPhase, grabbed time.Time) catalogv1alpha1.DownloadEntry {
		return catalogv1alpha1.DownloadEntry{
			ID: id, Release: catalogv1alpha1.DownloadRelease{GUID: guid}, Phase: phase, GrabbedAt: metav1.NewTime(grabbed),
		}
	}
	movie := func(es ...catalogv1alpha1.DownloadEntry) *catalogv1alpha1.Movie {
		return &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "m"}, Status: catalogv1alpha1.MovieStatus{Downloads: es}}
	}

	tests := []struct {
		name  string
		owner *catalogv1alpha1.Movie
		want  string
	}{
		{"the pick's grab, blocklisted in the second it was grabbed",
			movie(entry("own", "gone", commonv1.DownloadPhaseBlocklisted, created)), "own"},
		{"the pick's grab, blocklisted later",
			movie(entry("own", "gone", commonv1.DownloadPhaseBlocklisted, created.Add(time.Minute))), "own"},
		{"a tombstone from before the Search is no grab of the pick",
			movie(entry("tomb", "gone", commonv1.DownloadPhaseBlocklisted, created.Add(-time.Hour))), ""},
		{"past a tombstone, the override's own grab",
			movie(entry("tomb", "gone", commonv1.DownloadPhaseBlocklisted, created.Add(-time.Hour)),
				entry("over", "gone", commonv1.DownloadPhaseDownloading, created.Add(time.Second))), "over"},
		{"an earlier live grab of the release is the pick's",
			movie(entry("live", "gone", commonv1.DownloadPhaseSeeding, created.Add(-time.Hour))), "live"},
		{"another release's entry is not", movie(entry("other", "x", commonv1.DownloadPhaseBlocklisted, created)), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, entryFor(s, tt.owner, tt.owner, "gone"))
		})
	}
}
