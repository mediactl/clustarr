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
	"k8s.io/utils/ptr"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/torznab"
)

var selectNow = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// healthyIndexer is an Indexer every gate passes, so each table row can
// break exactly one thing.
func healthyIndexer(name string) indexv1alpha1.Indexer {
	return indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name},
		Spec:       indexv1alpha1.IndexerSpec{BaseURL: "https://tr.example", Priority: 25},
		Status: indexv1alpha1.IndexerStatus{
			Protocol: commonv1.ProtocolTorrent,
			Caps: &indexv1alpha1.Caps{
				Modes:      map[string][]string{"movie": {"imdbid", "q", "tmdbid"}},
				Categories: []indexv1alpha1.Category{{ID: 2000, Name: "Movies"}},
			},
		},
	}
}

func movieRequest() schema.SearchRequest {
	return schema.SearchRequest{
		Kind:       commonv1.MediaKindMovie,
		IDs:        map[string]string{commonv1.IDKeyTMDB: "27205"},
		Categories: []int32{2000},
	}
}

func TestSelectCandidatesGates(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*indexv1alpha1.Indexer)
		request func(*schema.SearchRequest)
		want    string
	}{
		{name: "healthy", want: ""},
		{
			name:   "disabled",
			mutate: func(i *indexv1alpha1.Indexer) { i.Spec.Enabled = ptr.To(false) },
			want:   skipDisabled,
		},
		{
			name:   "automatic search disabled",
			mutate: func(i *indexv1alpha1.Indexer) { i.Spec.EnableAutomaticSearch = ptr.To(false) },
			want:   skipNoAuto,
		},
		{
			name:    "interactive search disabled",
			mutate:  func(i *indexv1alpha1.Indexer) { i.Spec.EnableInteractiveSearch = ptr.To(false) },
			request: func(r *schema.SearchRequest) { r.UserInvoked = true },
			want:    skipNoInteractive,
		},
		{
			// The switches are per-trigger: an indexer that opts out of
			// automatic searches is still available interactively.
			name:    "automatic switch does not gate an interactive search",
			mutate:  func(i *indexv1alpha1.Indexer) { i.Spec.EnableAutomaticSearch = ptr.To(false) },
			request: func(r *schema.SearchRequest) { r.UserInvoked = true },
			want:    "",
		},
		{
			name:    "protocol not requested",
			request: func(r *schema.SearchRequest) { r.Protocols = []commonv1.Protocol{commonv1.ProtocolUsenet} },
			want:    skipProtocol,
		},
		{
			name:    "protocol requested",
			request: func(r *schema.SearchRequest) { r.Protocols = []commonv1.Protocol{commonv1.ProtocolTorrent} },
			want:    "",
		},
		{
			name:   "caps not probed",
			mutate: func(i *indexv1alpha1.Indexer) { i.Status.Caps = nil },
			want:   skipNoCaps,
		},
		{
			name: "mode not supported",
			mutate: func(i *indexv1alpha1.Indexer) {
				i.Status.Caps.Modes = map[string][]string{"tvsearch": {"tvdbid"}}
			},
			want: "does not support mode movie",
		},
		{
			name: "no category in common",
			mutate: func(i *indexv1alpha1.Indexer) {
				i.Status.Caps.Categories = []indexv1alpha1.Category{{ID: 7000, Name: "Other"}}
			},
			want: skipNoCategory,
		},
		{
			name: "unhealthy",
			mutate: func(i *indexv1alpha1.Indexer) {
				until := metav1.NewTime(selectNow.Add(time.Hour))
				i.Status.DisabledUntil = &until
			},
			want: skipUnhealthy,
		},
		{
			name: "backoff expired",
			mutate: func(i *indexv1alpha1.Indexer) {
				until := metav1.NewTime(selectNow.Add(-time.Minute))
				i.Status.DisabledUntil = &until
			},
			want: "",
		},
		{
			name: "query limit reached",
			mutate: func(i *indexv1alpha1.Indexer) {
				i.Spec.Limits = &indexv1alpha1.Limits{QueryLimit: ptr.To(int32(50))}
				i.Status.QueriesInWindow = 50
			},
			want: skipQueryLimit,
		},
		{
			name: "under the query limit",
			mutate: func(i *indexv1alpha1.Indexer) {
				i.Spec.Limits = &indexv1alpha1.Limits{QueryLimit: ptr.To(int32(50))}
				i.Status.QueriesInWindow = 49
			},
			want: "",
		},
		{
			// A counter with no configured limit is observability only.
			name: "no configured limit never gates",
			mutate: func(i *indexv1alpha1.Indexer) {
				i.Status.QueriesInWindow = 100000
			},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := healthyIndexer("a")
			if tt.mutate != nil {
				tt.mutate(&idx)
			}
			req := movieRequest()
			if tt.request != nil {
				tt.request(&req)
			}
			got := selectCandidates([]indexv1alpha1.Indexer{idx}, req, torznab.ModeMovieSearch, selectNow)
			require.Len(t, got, 1, "an in-scope indexer is always a candidate, skipped or not")
			require.Equal(t, tt.want, got[0].Skip)
		})
	}
}

// An indexer the caller did not ask for is not a candidate AT ALL: it gets no
// outcome, because the caller keeps only a hundred and the ones it asked for
// must not be crowded out.
func TestSelectCandidatesScoping(t *testing.T) {
	a, b := healthyIndexer("a"), healthyIndexer("b")
	other := healthyIndexer("a")
	other.Namespace = "elsewhere"
	all := []indexv1alpha1.Indexer{a, b, other}

	t.Run("an unnamed indexer is absent, not skipped", func(t *testing.T) {
		req := movieRequest()
		req.IndexerRefs = []schema.Ref{{Namespace: "media", Name: "a"}}
		got := selectCandidates(all, req, torznab.ModeMovieSearch, selectNow)
		require.Len(t, got, 1)
		require.Equal(t, "a", got[0].Indexer.Name)
		require.Equal(t, "media", got[0].Indexer.Namespace)
	})

	t.Run("a ref in another namespace matches nothing", func(t *testing.T) {
		req := movieRequest()
		req.IndexerRefs = []schema.Ref{{Namespace: "other", Name: "a"}}
		require.Empty(t, selectCandidates(all, req, torznab.ModeMovieSearch, selectNow))
	})

	t.Run("no refs means every indexer", func(t *testing.T) {
		require.Len(t, selectCandidates(all, movieRequest(), torznab.ModeMovieSearch, selectNow), 3)
	})
}

// The id-parameter gate cannot live in selectCandidates because it needs
// buildQuery's verdict.
func TestResolveQuery(t *testing.T) {
	idx := healthyIndexer("a")
	idx.Status.Caps.Modes = map[string][]string{"movie": {"q"}}
	c := candidate{Indexer: &idx}

	_, skip := resolveQuery(c, movieRequest(), torznab.ModeMovieSearch, 500)
	require.Equal(t, skipNoIDParam, skip)

	ok := healthyIndexer("b")
	q, skip := resolveQuery(candidate{Indexer: &ok}, movieRequest(), torznab.ModeMovieSearch, 500)
	require.Empty(t, skip)
	require.Equal(t, "27205", q.TMDBID)
}

// A request with no categories at all must not be gated by the category
// intersection -- there is nothing to intersect.
func TestSelectCandidatesWithNoRequestedCategories(t *testing.T) {
	idx := healthyIndexer("a")
	idx.Status.Caps.Categories = []indexv1alpha1.Category{{ID: 7000, Name: "Other"}}
	req := movieRequest()
	req.Categories = nil

	got := selectCandidates([]indexv1alpha1.Indexer{idx}, req, torznab.ModeMovieSearch, selectNow)
	require.Len(t, got, 1)
	require.Empty(t, got[0].Skip)
}
