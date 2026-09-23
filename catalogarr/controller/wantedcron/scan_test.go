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
	"github.com/mediactl/clustarr/pkg/events/schema"
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

// candidatesOf converts fixtures through the same per-kind converters
// ListCandidates uses.
func candidatesOf(movies []catalogv1alpha1.Movie, episodes []catalogv1alpha1.Episode) []Candidate {
	var out []Candidate
	for i := range movies {
		out = append(out, movieCandidate(&movies[i]))
	}
	for i := range episodes {
		out = append(out, episodeCandidate(&episodes[i]))
	}
	return out
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
			assert.Equal(t, c.want, eligibleNamespaces(candidatesOf(c.movies, c.episodes), scanNow))
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
	assert.Empty(t, eligibleNamespaces(candidatesOf([]catalogv1alpha1.Movie{m}, nil), scanNow))

	old := metav1.NewTime(scanNow.Add(-8 * time.Hour))
	m.Status.LastSearchedAt = &old
	assert.Equal(t, []string{"media"}, eligibleNamespaces(candidatesOf([]catalogv1alpha1.Movie{m}, nil), scanNow))
}

// TestNonVideoCandidates pins the carried "automatic search is movie and
// episode only" defect from wantedcron's side: an album, a book, an
// audiobook or an issue that is missing something now wakes its namespace,
// through the same Wanted/CutoffUnmet reading as a movie.
func TestNonVideoCandidates(t *testing.T) {
	album := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Namespace: "music", Name: "kid-a"},
		Status:     catalogv1alpha1.AlbumStatus{Phase: catalogv1alpha1.AlbumPhaseWanted},
	}
	book := &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Namespace: "books", Name: "dune"},
		Status:     catalogv1alpha1.BookStatus{Phase: catalogv1alpha1.BookPhaseCutoffUnmet},
	}
	audiobook := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Namespace: "books", Name: "dune-audible"},
		Status:     catalogv1alpha1.AudiobookStatus{Phase: catalogv1alpha1.AudiobookPhaseImported},
	}
	assert.Equal(t, schema.SearchReasonMissing, albumCandidate(album).Reason)
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "kid-a"}, albumCandidate(album).Ref)
	assert.Equal(t, schema.SearchReasonCutoffUnmet, bookCandidate(book).Reason)
	assert.Empty(t, audiobookCandidate(audiobook).Reason, "an imported audiobook is not wanted")

	assert.Equal(t, []string{"books", "music"},
		eligibleNamespaces([]Candidate{albumCandidate(album), bookCandidate(book), audiobookCandidate(audiobook)}, scanNow),
		"a namespace holding only non-video items is woken")
	assert.False(t, bookCandidate(book).Due(scanNow, false), "an upgrade is searched only when the sweep asks for upgrades")
}

func TestIssueCandidate(t *testing.T) {
	past := metav1.NewTime(scanNow.Add(-24 * time.Hour))
	future := metav1.NewTime(scanNow.Add(30 * 24 * time.Hour))
	issue := func(state catalogv1alpha1.IssueState, date *metav1.Time, cutoff *metav1.ConditionStatus, monitored bool) *catalogv1alpha1.Issue {
		iss := &catalogv1alpha1.Issue{
			ObjectMeta: metav1.ObjectMeta{Namespace: "comics", Name: "saga-050"},
			Spec:       catalogv1alpha1.IssueSpec{ComicRef: "saga", Number: "50", Monitored: &monitored},
			Status:     catalogv1alpha1.IssueStatus{State: state, Date: date},
		}
		if cutoff != nil {
			iss.Status.Conditions = []metav1.Condition{{Type: catalogv1alpha1.IssueConditionCutoffMet, Status: *cutoff}}
		}
		return iss
	}
	f, tr := metav1.ConditionFalse, metav1.ConditionTrue

	cases := []struct {
		name string
		iss  *catalogv1alpha1.Issue
		want schema.SearchReason
	}{
		{"a wanted issue that is out", issue(catalogv1alpha1.IssueStateWanted, &past, nil, true), schema.SearchReasonMissing},
		{"a wanted issue with no date counts as out", issue(catalogv1alpha1.IssueStateWanted, nil, nil, true), schema.SearchReasonMissing},
		{"a wanted issue not out yet is not searched", issue(catalogv1alpha1.IssueStateWanted, &future, nil, true), ""},
		{"an unmonitored issue is never searched", issue(catalogv1alpha1.IssueStateWanted, &past, nil, false), ""},
		{"downloaded below the cutoff is an upgrade", issue(catalogv1alpha1.IssueStateDownloaded, &past, &f, true), schema.SearchReasonCutoffUnmet},
		{"downloaded at the cutoff is done", issue(catalogv1alpha1.IssueStateDownloaded, &past, &tr, true), ""},
		{"a cutoff never evaluated is not assumed unmet", issue(catalogv1alpha1.IssueStateDownloaded, &past, nil, true), ""},
		{"snatched is already being handled", issue(catalogv1alpha1.IssueStateSnatched, &past, nil, true), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := issueCandidate(c.iss, scanNow)
			assert.Equal(t, c.want, got.Reason)
			assert.Equal(t, commonv1.MediaKindIssue, got.Ref.Kind)
		})
	}
}
