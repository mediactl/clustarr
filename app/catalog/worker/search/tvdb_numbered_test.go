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
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// byNumberRPC answers a search by the season and episode it asks for.
type byNumberRPC struct {
	asked   [][2]int32
	answers map[[2]int32]schema.SearchResponse
	fail    map[[2]int32]error
}

func (r *byNumberRPC) Search(_ context.Context, req schema.SearchRequest) (schema.SearchResponse, error) {
	k := [2]int32{ptr.Deref(req.Season, -1), ptr.Deref(req.Episode, -1)}
	r.asked = append(r.asked, k)
	return r.answers[k], r.fail[k]
}

func rel(indexer, guid, title string) schema.Release {
	return schema.Release{Info: commonv1.ReleaseInfo{IndexerRef: indexer, GUID: guid, Title: title}}
}

func outcome(indexer string, status schema.SearchOutcomeStatus, n int32) schema.SearchOutcome {
	return schema.SearchOutcome{IndexerRef: schema.Ref{Namespace: "media", Name: indexer}, Status: status, Releases: n}
}

// A person's search for an episode whose scene numbering differs from
// TVDB's asks for both: asked for scene S02E07 alone, nzb-life never
// returned "S02E14 Independence Dick" for Panty & Stocking with Garterbelt
// S02E14 (talos, 2026-11-26). The request is the one BuildSearchRequest
// makes for the snapshot's ids; an automatic search, an index-only one and
// an episode numbered alike both ways ask once.
func TestAPersonsSearchAlsoAsksForTheEpisodesTVDBNumbering(t *testing.T) {
	ids := TargetIDs{TvdbID: 188551, Season: ptr.To[int32](2), Episode: ptr.To[int32](7), TVDBSeason: ptr.To[int32](2), TVDBEpisode: ptr.To[int32](14), Title: "Panty & Stocking with Garterbelt"}
	req := BuildSearchRequest("media", commonv1.MediaKindEpisode, ids, 100, true, nil, nil)
	person := schema.SearchTask{UserInvoked: true}

	more := tvdbNumbered(req, ids, person)
	require.Len(t, more, 1)
	require.Equal(t, [2]int32{2, 14}, [2]int32{*more[0].Season, *more[0].Episode})
	require.Equal(t, req.IDs, more[0].IDs)
	require.Equal(t, [2]int32{2, 7}, [2]int32{*req.Season, *req.Episode}, "the first request is not written")

	require.Empty(t, tvdbNumbered(req, ids, schema.SearchTask{}), "an automatic search asks once")
	require.Empty(t, tvdbNumbered(req, ids, schema.SearchTask{UserInvoked: true, IndexOnly: true}))
	same := ids
	same.TVDBSeason, same.TVDBEpisode = nil, nil
	require.Empty(t, tvdbNumbered(req, same, person), "numbered alike both ways")

	broadcast := rel("nzb-life", "g1", "PANTY.and.STOCKING.With.GARTERBELT.S02E07.1080p.AMZN.WEB-DL.DDP5.1.H.264-ppkhoa")
	segment := rel("nzb-life", "g2", "PANTY and STOCKING with GARTERBELT S02E14 Independence Dick 1080p AMZN WEB-DL DUAL DDP5 1 H 264-Kitsune mkv-xpost")
	rpc := &byNumberRPC{answers: map[[2]int32]schema.SearchResponse{
		{2, 7}: {
			Releases: []schema.Release{broadcast},
			Outcomes: []schema.SearchOutcome{outcome("nzb-life", schema.SearchOutcomeOK, 1), outcome("nzbgeek", schema.SearchOutcomeTimeout, 0)},
		},
		{2, 14}: {
			Releases: []schema.Release{segment, broadcast, rel("nzbgeek", "g1", "the same guid on another indexer")},
			Outcomes: []schema.SearchOutcome{outcome("nzb-life", schema.SearchOutcomeOK, 2), outcome("nzbgeek", schema.SearchOutcomeOK, 1)},
		},
	}}
	w := &Worker{RPC: rpc}
	resp, err := w.searchLive(context.Background(), gateMessage{}, person, req, more...)
	require.NoError(t, err)
	require.Equal(t, [][2]int32{{2, 7}, {2, 14}}, rpc.asked)
	titles := make([]string, 0, len(resp.Releases))
	for _, r := range resp.Releases {
		titles = append(titles, r.Info.IndexerRef+" "+r.Info.GUID)
	}
	require.Equal(t, []string{"nzb-life g1", "nzb-life g2", "nzbgeek g1"}, titles, "each release once, by indexer and guid")
	require.Equal(t, []schema.SearchOutcome{outcome("nzb-life", schema.SearchOutcomeOK, 2), outcome("nzbgeek", schema.SearchOutcomeOK, 1)}, resp.Outcomes,
		"an indexer's count is what it returned in all, and one that answered the second time answered")

	// The second request failing leaves the first's answer.
	rpc = &byNumberRPC{answers: rpc.answers, fail: map[[2]int32]error{{2, 14}: errors.New("index is down")}}
	w = &Worker{RPC: rpc}
	resp, err = w.searchLive(context.Background(), gateMessage{}, person, req, more...)
	require.NoError(t, err)
	require.Len(t, resp.Releases, 1)
}
