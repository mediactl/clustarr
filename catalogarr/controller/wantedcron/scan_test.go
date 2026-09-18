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

package wantedcron

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

var scanNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func movie(ns, name string, phase catalogv1alpha1.MoviePhase, attempts commonv1.Attempts) catalogv1alpha1.Movie {
	return catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Status:     catalogv1alpha1.MovieStatus{Phase: phase, SearchAttempts: attempts},
	}
}

func episode(ns, name string, phase catalogv1alpha1.EpisodePhase, attempts commonv1.Attempts) catalogv1alpha1.Episode {
	return catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Status:     catalogv1alpha1.EpisodeStatus{Phase: phase, SearchAttempts: attempts},
	}
}

func at(d time.Duration) commonv1.Attempts {
	t := metav1.NewTime(scanNow.Add(d))
	return commonv1.Attempts{Latest: &t, Count: 1}
}

func TestEligibleNamespaces(t *testing.T) {
	cases := []struct {
		name     string
		movies   []catalogv1alpha1.Movie
		episodes []catalogv1alpha1.Episode
		want     []string
	}{
		{
			name: "no items at all",
			want: []string{},
		},
		{
			name:   "a never-searched Wanted movie wakes its namespace",
			movies: []catalogv1alpha1.Movie{movie("media", "a", catalogv1alpha1.MoviePhaseWanted, commonv1.Attempts{})},
			want:   []string{"media"},
		},
		{
			name:   "CutoffUnmet counts too: an upgrade is still wanted",
			movies: []catalogv1alpha1.Movie{movie("media", "a", catalogv1alpha1.MoviePhaseCutoffUnmet, commonv1.Attempts{})},
			want:   []string{"media"},
		},
		{
			name: "every other phase is skipped",
			movies: []catalogv1alpha1.Movie{
				movie("media", "imported", catalogv1alpha1.MoviePhaseImported, commonv1.Attempts{}),
				movie("media", "downloading", catalogv1alpha1.MoviePhaseDownloading, commonv1.Attempts{}),
				movie("media", "delayed", catalogv1alpha1.MoviePhaseDelayed, commonv1.Attempts{}),
				movie("media", "unmonitored", catalogv1alpha1.MoviePhaseUnmonitored, commonv1.Attempts{}),
				movie("media", "unavailable", catalogv1alpha1.MoviePhaseUnavailable, commonv1.Attempts{}),
				movie("media", "pending", catalogv1alpha1.MoviePhasePending, commonv1.Attempts{}),
			},
			want: []string{},
		},
		{
			name:   "a Wanted movie searched an hour ago is inside its 6h gap",
			movies: []catalogv1alpha1.Movie{movie("media", "a", catalogv1alpha1.MoviePhaseWanted, at(-time.Hour))},
			want:   []string{},
		},
		{
			name:   "the same movie seven hours later is eligible again",
			movies: []catalogv1alpha1.Movie{movie("media", "a", catalogv1alpha1.MoviePhaseWanted, at(-7*time.Hour))},
			want:   []string{"media"},
		},
		{
			name: "one eligible item is enough to wake a namespace full of ineligible ones",
			movies: []catalogv1alpha1.Movie{
				movie("media", "recent", catalogv1alpha1.MoviePhaseWanted, at(-time.Hour)),
				movie("media", "stale", catalogv1alpha1.MoviePhaseWanted, at(-7*time.Hour)),
			},
			want: []string{"media"},
		},
		{
			name: "episodes wake their own namespaces, and output is sorted",
			movies: []catalogv1alpha1.Movie{
				movie("zulu", "a", catalogv1alpha1.MoviePhaseWanted, commonv1.Attempts{}),
			},
			episodes: []catalogv1alpha1.Episode{
				episode("alpha", "s01e01", catalogv1alpha1.EpisodePhaseWanted, commonv1.Attempts{}),
				episode("mike", "s01e01", catalogv1alpha1.EpisodePhaseUnaired, commonv1.Attempts{}),
			},
			want: []string{"alpha", "zulu"},
		},
		{
			name: "a namespace with only backed-off episodes stays asleep",
			episodes: []catalogv1alpha1.Episode{
				episode("media", "s01e01", catalogv1alpha1.EpisodePhaseWanted, at(-2*time.Hour)),
			},
			want: []string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, eligibleNamespaces(c.movies, c.episodes, scanNow))
		})
	}
}

// TestEligibleNamespaces_LastSearchedAtIsHonoured: an item whose searcher
// recorded only the flat status.lastSearchedAt -- with no Attempts.Latest --
// is still inside its gap. Ignoring that field would re-search an item
// minutes after the last attempt.
func TestEligibleNamespaces_LastSearchedAtIsHonoured(t *testing.T) {
	recent := metav1.NewTime(scanNow.Add(-5 * time.Minute))
	m := movie("media", "a", catalogv1alpha1.MoviePhaseWanted, commonv1.Attempts{})
	m.Status.LastSearchedAt = &recent
	assert.Empty(t, eligibleNamespaces([]catalogv1alpha1.Movie{m}, nil, scanNow))

	old := metav1.NewTime(scanNow.Add(-8 * time.Hour))
	m.Status.LastSearchedAt = &old
	assert.Equal(t, []string{"media"}, eligibleNamespaces([]catalogv1alpha1.Movie{m}, nil, scanNow))
}
