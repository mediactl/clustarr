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
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// SweptKinds is every kind a sweep searches for: the kinds the search worker
// can snapshot, identify and decide (app/catalog/worker/search.Searchable).
// The containers -- series, artist, author, comic -- are not searched for
// themselves; their episodes, albums, books and issues are.
var SweptKinds = []commonv1.MediaKind{
	commonv1.MediaKindMovie,
	commonv1.MediaKindEpisode,
	commonv1.MediaKindAlbum,
	commonv1.MediaKindBook,
	commonv1.MediaKindAudiobook,
	commonv1.MediaKindIssue,
}

// Candidate is one catalog item as a sweep sees it: which item, why a search
// for it would run (empty when nothing about it is wanted), and its search
// attempts so far.
//
// Both halves of the sweep read items through this one type -- this
// package's eligibleNamespaces, which decides which namespaces to wake, and
// the search worker's WantedScan expansion, which decides which items in a
// woken namespace to search -- so the two cannot disagree about what is
// wanted. (They used to restate the phase predicates side by side, which is
// how a non-video kind came to be wanted by neither.)
type Candidate struct {
	Namespace string
	Ref       commonv1.MediaRef
	UID       string
	// Reason is SearchReasonMissing when the item has no file,
	// SearchReasonCutoffUnmet when it has one below its profile's cutoff,
	// and "" when it is not wanted at all.
	Reason schema.SearchReason
	// Attempts is status.searchAttempts with status.lastSearchedAt folded
	// in (searchAttempts).
	Attempts commonv1.Attempts
}

// Due reports whether the candidate should be searched at now: it is wanted
// -- for an upgrade only when cutoffUnmet asks for upgrades -- and its
// per-item backoff has elapsed.
func (c Candidate) Due(now time.Time, cutoffUnmet bool) bool {
	switch c.Reason {
	case schema.SearchReasonMissing:
	case schema.SearchReasonCutoffUnmet:
		if !cutoffUnmet {
			return false
		}
	default:
		return false
	}
	return Eligible(c.Attempts, now)
}

// ListCandidates lists every item of the kinds want admits, through c and
// with opts (a namespace, typically), as Candidates. now is the instant an
// issue's release is judged against.
func ListCandidates(ctx context.Context, c client.Reader, want func(commonv1.MediaKind) bool, now time.Time, opts ...client.ListOption) ([]Candidate, error) {
	var out []Candidate
	if want(commonv1.MediaKindMovie) {
		var l catalogv1alpha1.MovieList
		if err := c.List(ctx, &l, opts...); err != nil {
			return nil, fmt.Errorf("list movies: %w", err)
		}
		for i := range l.Items {
			out = append(out, movieCandidate(&l.Items[i]))
		}
	}
	if want(commonv1.MediaKindEpisode) {
		var l catalogv1alpha1.EpisodeList
		if err := c.List(ctx, &l, opts...); err != nil {
			return nil, fmt.Errorf("list episodes: %w", err)
		}
		for i := range l.Items {
			out = append(out, episodeCandidate(&l.Items[i]))
		}
	}
	if want(commonv1.MediaKindAlbum) {
		var l catalogv1alpha1.AlbumList
		if err := c.List(ctx, &l, opts...); err != nil {
			return nil, fmt.Errorf("list albums: %w", err)
		}
		for i := range l.Items {
			out = append(out, albumCandidate(&l.Items[i]))
		}
	}
	if want(commonv1.MediaKindBook) {
		var l catalogv1alpha1.BookList
		if err := c.List(ctx, &l, opts...); err != nil {
			return nil, fmt.Errorf("list books: %w", err)
		}
		for i := range l.Items {
			out = append(out, bookCandidate(&l.Items[i]))
		}
	}
	if want(commonv1.MediaKindAudiobook) {
		var l catalogv1alpha1.AudiobookList
		if err := c.List(ctx, &l, opts...); err != nil {
			return nil, fmt.Errorf("list audiobooks: %w", err)
		}
		for i := range l.Items {
			out = append(out, audiobookCandidate(&l.Items[i]))
		}
	}
	if want(commonv1.MediaKindIssue) {
		var l catalogv1alpha1.IssueList
		if err := c.List(ctx, &l, opts...); err != nil {
			return nil, fmt.Errorf("list issues: %w", err)
		}
		for i := range l.Items {
			out = append(out, issueCandidate(&l.Items[i], now))
		}
	}
	return out, nil
}

func candidate(obj metav1.Object, kind commonv1.MediaKind, reason schema.SearchReason, a commonv1.Attempts, last *metav1.Time) Candidate {
	return Candidate{
		Namespace: obj.GetNamespace(),
		Ref:       commonv1.MediaRef{Kind: kind, Name: obj.GetName()},
		UID:       string(obj.GetUID()),
		Reason:    reason,
		Attempts:  searchAttempts(a, last),
	}
}

// phaseReason is the reason shared by every kind with a Wanted/CutoffUnmet
// phase pair: Wanted means no file at all, CutoffUnmet a file below the
// profile's cutoff. Every other phase is either already handled
// (Downloading, Delayed, Imported), final (Transcoded: a transcoded Movie or
// Episode file is never upgraded automatically, CLAUDE.md "Transcoding"),
// deliberately out of scope (Unmonitored) or not yet actionable (Pending,
// Unavailable, Unaired, CutoffUnevaluated).
func phaseReason[P ~string](p, wanted, cutoffUnmet P) schema.SearchReason {
	switch p {
	case wanted:
		return schema.SearchReasonMissing
	case cutoffUnmet:
		return schema.SearchReasonCutoffUnmet
	default:
		return ""
	}
}

func movieCandidate(m *catalogv1alpha1.Movie) Candidate {
	r := phaseReason(m.Status.Phase, catalogv1alpha1.MoviePhaseWanted, catalogv1alpha1.MoviePhaseCutoffUnmet)
	return candidate(m, commonv1.MediaKindMovie, r, m.Status.SearchAttempts, m.Status.LastSearchedAt)
}

func episodeCandidate(e *catalogv1alpha1.Episode) Candidate {
	r := phaseReason(e.Status.Phase, catalogv1alpha1.EpisodePhaseWanted, catalogv1alpha1.EpisodePhaseCutoffUnmet)
	return candidate(e, commonv1.MediaKindEpisode, r, e.Status.SearchAttempts, e.Status.LastSearchedAt)
}

func albumCandidate(a *catalogv1alpha1.Album) Candidate {
	r := phaseReason(a.Status.Phase, catalogv1alpha1.AlbumPhaseWanted, catalogv1alpha1.AlbumPhaseCutoffUnmet)
	return candidate(a, commonv1.MediaKindAlbum, r, a.Status.SearchAttempts, a.Status.LastSearchedAt)
}

func bookCandidate(b *catalogv1alpha1.Book) Candidate {
	r := phaseReason(b.Status.Phase, catalogv1alpha1.BookPhaseWanted, catalogv1alpha1.BookPhaseCutoffUnmet)
	return candidate(b, commonv1.MediaKindBook, r, b.Status.SearchAttempts, b.Status.LastSearchedAt)
}

func audiobookCandidate(ab *catalogv1alpha1.Audiobook) Candidate {
	r := phaseReason(ab.Status.Phase, catalogv1alpha1.AudiobookPhaseWanted, catalogv1alpha1.AudiobookPhaseCutoffUnmet)
	return candidate(ab, commonv1.MediaKindAudiobook, r, ab.Status.SearchAttempts, ab.Status.LastSearchedAt)
}

// issueCandidate reads an Issue, which has no phase: its IssueState plus its
// cover date stand in for one (app/catalog/controller/issue.State).
//
//   - Missing: monitored, state wanted, and out by now. The issue controller
//     reports an unreleased monitored issue as wanted too (IssueState has no
//     "unreleased" member), so the date is what keeps a sweep from searching
//     for an issue months before its store date. An undated issue counts as
//     out, as the search worker's availability rule has it.
//   - CutoffUnmet: monitored, downloaded, and its CutoffMet condition is
//     present and False. An issue whose cutoff was never evaluated (no
//     condition) is not assumed to be below it: guessing "below" would
//     re-search every downloaded issue in the library every sweep.
func issueCandidate(iss *catalogv1alpha1.Issue, now time.Time) Candidate {
	var r schema.SearchReason
	if ptr.Deref(iss.Spec.Monitored, true) {
		switch iss.Status.State {
		case catalogv1alpha1.IssueStateWanted:
			if iss.Status.Date == nil || !iss.Status.Date.After(now) {
				r = schema.SearchReasonMissing
			}
		case catalogv1alpha1.IssueStateDownloaded:
			for _, c := range iss.Status.Conditions {
				if c.Type == catalogv1alpha1.IssueConditionCutoffMet && c.Status == metav1.ConditionFalse {
					r = schema.SearchReasonCutoffUnmet
				}
			}
		}
	}
	return candidate(iss, commonv1.MediaKindIssue, r, iss.Status.SearchAttempts, iss.Status.LastSearchedAt)
}
