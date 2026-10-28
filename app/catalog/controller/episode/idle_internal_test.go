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

package episode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// A manager's start enqueues every Episode the cache lists (owner,
// 2026-10-28: 28,945 of a 39,296-key backlog, 16,699 of them unmonitored).
// An Episode whose pass would change nothing -- unmonitored, or transcoded,
// with its spec observed and nothing in flight -- is left to its watches.
func TestAnIdleEpisodeIsSkippedAtAManagersStart(t *testing.T) {
	base := func(phase catalogv1alpha1.EpisodePhase) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "show-s01e01", Generation: 3},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "show", SeasonNumber: 1, EpisodeNumber: 1},
			Status:     catalogv1alpha1.EpisodeStatus{ObservedGeneration: 3, Phase: phase},
		}
	}
	series := func(monitored bool) *catalogv1alpha1.Series {
		return &catalogv1alpha1.Series{Spec: catalogv1alpha1.SeriesSpec{Monitored: ptr.To(monitored)}}
	}
	ref := "show-entry"
	for _, tc := range []struct {
		name   string
		ep     func() *catalogv1alpha1.Episode
		series *catalogv1alpha1.Series
		idle   bool
	}{
		{"unmonitored itself", func() *catalogv1alpha1.Episode {
			e := base(catalogv1alpha1.EpisodePhaseUnmonitored)
			e.Spec.Monitored = ptr.To(false)
			return e
		}, series(true), true},
		{"its series unmonitored", func() *catalogv1alpha1.Episode { return base(catalogv1alpha1.EpisodePhaseUnmonitored) }, series(false), true},
		{"reads unmonitored but both are monitored", func() *catalogv1alpha1.Episode { return base(catalogv1alpha1.EpisodePhaseUnmonitored) }, series(true), false},
		{"transcoded", func() *catalogv1alpha1.Episode { return base(catalogv1alpha1.EpisodePhaseTranscoded) }, series(true), true},
		{"wanted", func() *catalogv1alpha1.Episode { return base(catalogv1alpha1.EpisodePhaseWanted) }, series(true), false},
		{"unaired", func() *catalogv1alpha1.Episode { return base(catalogv1alpha1.EpisodePhaseUnaired) }, series(true), false},
		{"imported, not yet transcoded", func() *catalogv1alpha1.Episode { return base(catalogv1alpha1.EpisodePhaseImported) }, series(true), false},
		{"spec not yet observed", func() *catalogv1alpha1.Episode {
			e := base(catalogv1alpha1.EpisodePhaseTranscoded)
			e.Generation = 4
			return e
		}, series(true), false},
		{"a grab in flight", func() *catalogv1alpha1.Episode {
			e := base(catalogv1alpha1.EpisodePhaseTranscoded)
			e.Status.ActiveDownloadRef, e.Status.DownloadPhase = &ref, commonv1.DownloadPhaseDownloading
			return e
		}, series(true), false},
		{"a pending grab", func() *catalogv1alpha1.Episode {
			e := base(catalogv1alpha1.EpisodePhaseUnmonitored)
			e.Spec.Monitored = ptr.To(false)
			e.Status.PendingGrab = &catalogv1alpha1.PendingGrab{}
			return e
		}, series(true), false},
		{"a search in flight", func() *catalogv1alpha1.Episode {
			e := base(catalogv1alpha1.EpisodePhaseTranscoded)
			e.Status.SearchDispatch = &catalogv1alpha1.Dispatch{Seq: 2, AnsweredSeq: 1}
			return e
		}, series(true), false},
		{"a dub still wanted", func() *catalogv1alpha1.Episode {
			e := base(catalogv1alpha1.EpisodePhaseTranscoded)
			e.Status.Audio = &catalogv1alpha1.AudioState{Missing: []string{"en"}}
			return e
		}, series(true), false},
		{"no series in the cache", func() *catalogv1alpha1.Episode { return base(catalogv1alpha1.EpisodePhaseUnmonitored) }, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.idle, idleAtStart(tc.ep(), tc.series))
		})
	}
}
