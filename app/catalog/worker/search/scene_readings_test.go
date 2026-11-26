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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/titleindex"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/metadata/scenemap"
	"github.com/mediactl/clustarr/pkg/release"
)

// The identity check tells a release numbered by TVDB from one numbered by
// broadcast by its episode's title, and reads a season's own scene title:
// SceneReadings gives it the rows titled from the series' Episodes and
// TheXEM's scene season names. The proof is the decision's own reading of
// the two releases talos grabbed for the wrong episodes (2026-11-26).
func TestSceneReadingsTitleTheRowsAndNameTheSceneSeasons(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "show"},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 188551},
	}
	objs := []client.Object{series}
	titles := map[int32]string{7: "Daten City President", 14: "Independence Dick", 15: "Longest Bitch Yard"}
	for n, title := range titles {
		ep := &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: fmt.Sprintf("show-s02e%02d", n)},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "show", SeasonNumber: 2, EpisodeNumber: n},
		}
		ep.Status.Title = title
		objs = append(objs, ep)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithIndex(&catalogv1alpha1.Episode{}, titleindex.IndexEpisodeSeriesSeason, titleindex.EpisodeSeasonKeys).Build()
	n := func(s, e int) scenemap.Numbering { return scenemap.Numbering{Season: s, Episode: e} }
	src := sceneSourceFunc(func(_ context.Context, tvdbID int64) (*scenemap.Map, error) {
		return &scenemap.Map{
			TVDBID: tvdbID,
			Mappings: []scenemap.Mapping{
				{Scene: n(2, 4), TVDB: n(2, 7)}, {Scene: n(2, 7), TVDB: n(2, 14)}, {Scene: n(2, 7), TVDB: n(2, 15)},
			},
			Names: []scenemap.SceneName{{Title: "New Panty & Stocking with Garterbelt", Season: ptr.To(2)}, {Title: "Panty & Stocking"}},
		}, nil
	})
	ctx := context.Background()
	rows := SceneMappings(ctx, src, series.Spec.TvdbID)
	require.Len(t, rows, 3)

	var id decision.Identity
	id.SceneMappings, id.SceneSeasonNames = SceneReadings(ctx, c, src, series, rows)
	require.Equal(t, []string{"Daten City President", "Independence Dick", "Longest Bitch Yard"},
		[]string{id.SceneMappings[0].Title, id.SceneMappings[1].Title, id.SceneMappings[2].Title})
	require.Equal(t, []decision.SceneSeasonName{{Title: "New Panty & Stocking with Garterbelt", Season: 2}}, id.SceneSeasonNames,
		"a title naming the whole series names no season")
	require.Empty(t, rows[0].Title, "the table SceneMappings answered is not written")

	covers := func(title string) []decision.SeasonEpisode {
		p, err := release.Parse(title, release.Options{Kind: commonv1.MediaKindEpisode})
		require.NoError(t, err)
		return decision.CoverageFor(p, id).Episodes
	}
	require.Equal(t, []decision.SeasonEpisode{{Season: 2, Episode: 7}},
		covers("PANTY.and.STOCKING.with.GARTERBELT.S02E07.Daten.City.President.1080p.AMZN.WEB-DL.DUAL.DDP5.1.H.264-Kitsune.mkv-xpost"))
	require.Equal(t, []decision.SeasonEpisode{{Season: 2, Episode: 14}, {Season: 2, Episode: 15}},
		covers("New.PANTY.and.STOCKING.with.GARTERBELT.S01E07.14.15.1080p.AMZN.WEB-DL.DUAL.DDP5.1.H.265.MSubs-ToonsHub"))

	// With no source, and with no Episodes to title the rows from, the
	// table is returned as it was.
	got, names := SceneReadings(ctx, c, nil, series, rows)
	require.Equal(t, rows, got)
	require.Nil(t, names)
}
