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
	"strconv"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/newznab"
)

// SearchDeadline is the RPC deadline every federated search carries. Spec §5's
// clustarr.rpc.indexarr.search row: "single reply at min(deadline, 45s)".
const SearchDeadline = 45 * time.Second

// TargetIDs is the identity BuildSearchRequest needs for one catalog item. It
// is a flat value rather than the catalog object itself so the request
// builder stays a pure function testable without a cluster; Worker.snapshot
// is what fills it from a Movie or an Episode.
type TargetIDs struct {
	TmdbID  int64
	ImdbID  string
	TvdbID  int64
	Season  *int32
	Episode *int32
	// Anime marks a series whose episodes are numbered absolutely: Episode
	// then carries the absolute number and there is no season token.
	Anime bool
	Year  int32
	// OriginalLanguageTag is not part of the request; it rides along
	// because every caller that builds a TargetIDs also needs it for the
	// decision Target, and threading one value is cheaper than two parallel
	// structs. It is the BCP-47 tag exactly as
	// Movie/Series.status.metadata.originalLanguage stores it, NOT a Radarr
	// English display name -- decision.Target.OriginalLanguageTag is where
	// it lands and pkg/decision owns the one conversion into the
	// display-name vocabulary the catalogue speaks.
	OriginalLanguageTag string
}

// BuildSearchRequest renders one catalog item's identity into the federated
// search RPC request. indexerRefs and categories, when non-empty, override the
// newznab.ByKind(kind) default -- that is Search.spec.indexerRefs and
// Search.spec.categories on an interactive search.
//
// schema.SearchRequest (pkg/events/schema/index.go) has Season and Episode but
// no Absolute field, unlike spec §8.2's "tvdb plus season/episode/absolute".
// For an anime episode this function puts the absolute number in Episode and
// leaves Season nil, which is how *arr indexers key anime releases (absolute
// numbering, no season token). The generated payload type wins over the spec's
// literal wording; the design doc's own §5 table already shows this shape.
// ns scopes the search to one namespace's Indexers. It is always set here --
// the worker already recovered it from the envelope key -- because indexarr
// has no other source for it on an automatic search, where IndexerRefs is
// empty. Without it indexarr must list Indexers cluster-wide and can serve
// one namespace's media from another's indexer.
func BuildSearchRequest(ns string, kind commonv1.MediaKind, ids TargetIDs, limit int32, userInvoked bool, indexerRefs []schema.Ref, categories []int32) schema.SearchRequest {
	req := schema.SearchRequest{
		Namespace:      ns,
		Kind:           kind,
		Limit:          limit,
		DeadlineMillis: SearchDeadline.Milliseconds(),
		UserInvoked:    userInvoked,
		Year:           ids.Year,
		IndexerRefs:    indexerRefs,
	}
	if len(categories) > 0 {
		req.Categories = categories
	} else {
		req.Categories = expandCategoryIDs(newznab.ByKind(kind))
	}

	idmap := map[string]string{}
	switch kind {
	case commonv1.MediaKindMovie:
		if ids.TmdbID != 0 {
			idmap[commonv1.IDKeyTMDB] = strconv.FormatInt(ids.TmdbID, 10)
		}
		if ids.ImdbID != "" {
			idmap[commonv1.IDKeyIMDB] = ids.ImdbID
		}
	case commonv1.MediaKindEpisode:
		if ids.TvdbID != 0 {
			idmap[commonv1.IDKeyTVDB] = strconv.FormatInt(ids.TvdbID, 10)
		}
		req.Episode = ids.Episode
		if !ids.Anime {
			req.Season = ids.Season
		}
	}
	if len(idmap) > 0 {
		req.IDs = idmap
	}
	return req
}

func expandCategoryIDs(cats []newznab.CategoryID) []int32 {
	out := make([]int32, len(cats))
	for i, c := range cats {
		out[i] = int32(c)
	}
	return out
}
