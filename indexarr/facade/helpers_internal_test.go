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

package facade

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/torznab"
)

func TestBuildSearchRequestFillsEveryWireParam(t *testing.T) {
	q, err := url.ParseQuery("q=the+matrix&cat=2000,2010,notanumber&limit=50&imdbid=133093&tmdbid=603&tvdbid=121&season=1&ep=2")
	require.NoError(t, err)

	req := buildSearchRequest(q, torznab.ModeMovieSearch, 30*time.Second)
	require.Equal(t, "the matrix", req.Text)
	require.Equal(t, commonv1.MediaKindMovie, req.Kind)
	require.Equal(t, []int32{2000, 2010}, req.Categories, "a non-numeric category token is dropped, not fatal")
	require.EqualValues(t, 50, req.Limit)
	require.Equal(t, "tt133093", req.IDs[commonv1.IDKeyIMDB], "a bare numeric imdbid is tt-prefixed")
	require.Equal(t, "603", req.IDs[commonv1.IDKeyTMDB])
	require.Equal(t, "121", req.IDs[commonv1.IDKeyTVDB])
	require.NotNil(t, req.Season)
	require.EqualValues(t, 1, *req.Season)
	require.NotNil(t, req.Episode)
	require.EqualValues(t, 2, *req.Episode)
	require.EqualValues(t, 30000, req.DeadlineMillis)
	require.True(t, req.UserInvoked)
}

func TestBuildSearchRequestKindForMode(t *testing.T) {
	q := url.Values{}
	require.Equal(t, commonv1.MediaKindMovie, buildSearchRequest(q, torznab.ModeMovieSearch, time.Second).Kind)
	require.Equal(t, commonv1.MediaKindEpisode, buildSearchRequest(q, torznab.ModeTVSearch, time.Second).Kind)
	require.Equal(t, commonv1.MediaKindBook, buildSearchRequest(q, torznab.ModeBookSearch, time.Second).Kind)
	require.Empty(t, buildSearchRequest(q, torznab.ModeSearch, time.Second).Kind)
	require.Empty(t, buildSearchRequest(q, torznab.ModeMusicSearch, time.Second).Kind)
}

func TestBuildSearchRequestAlreadyPrefixedIMDBIDIsUnchanged(t *testing.T) {
	q := url.Values{"imdbid": {"tt0133093"}}
	req := buildSearchRequest(q, torznab.ModeMovieSearch, time.Second)
	require.Equal(t, "tt0133093", req.IDs[commonv1.IDKeyIMDB])
}

func TestBuildSearchRequestNoIDsLeavesTheMapNil(t *testing.T) {
	req := buildSearchRequest(url.Values{}, torznab.ModeSearch, time.Second)
	require.Nil(t, req.IDs)
}

func TestLeadingInt(t *testing.T) {
	n, ok := leadingInt("5")
	require.True(t, ok)
	require.Equal(t, 5, n)

	n, ok = leadingInt("5-6")
	require.True(t, ok, "a range still yields its leading value")
	require.Equal(t, 5, n)

	_, ok = leadingInt("")
	require.False(t, ok)

	_, ok = leadingInt("abc")
	require.False(t, ok)
}

func TestParseInt32ListDropsUnparsableTokens(t *testing.T) {
	require.Equal(t, []int32{2000, 2010}, parseInt32List("2000, 2010,notanumber,"))
	require.Nil(t, parseInt32List(""))
}

func TestCapsFromIndexerWithNoProbedCapsIsEmptyButValid(t *testing.T) {
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "idx1"}}
	c := capsFromIndexer(idx)
	require.Equal(t, "idx1", c.ServerTitle)
	require.Nil(t, c.Modes)
	require.Nil(t, c.Categories)
}

func TestCapsFromIndexerConvertsModesAndCategories(t *testing.T) {
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: "idx1"},
		Status: indexv1alpha1.IndexerStatus{
			Caps: &indexv1alpha1.Caps{
				Modes:         map[string][]string{"movie": {"imdbid"}, "tvsearch": {"tvdbid", "season", "ep"}},
				LimitsMax:     100,
				LimitsDefault: 50,
				Categories: []indexv1alpha1.Category{
					{ID: 2000, Name: "Movies", Sub: []indexv1alpha1.SubCategory{{ID: 2040, Name: "Movies/HD"}}},
				},
			},
		},
	}
	c := capsFromIndexer(idx)
	require.Equal(t, 100, c.LimitsMax)
	require.Equal(t, 50, c.LimitsDefault)
	require.True(t, c.Modes[torznab.ModeMovieSearch].Available)
	require.Equal(t, []string{"imdbid"}, c.Modes[torznab.ModeMovieSearch].SupportedParams)
	require.True(t, c.Modes[torznab.ModeTVSearch].Available)
	_, hasSearch := c.Modes[torznab.ModeSearch]
	require.False(t, hasSearch, "an unprobed mode is simply absent, not Available:false")
	require.Len(t, c.Categories, 1)
	require.Equal(t, "Movies", c.Categories[0].Name)
	require.Len(t, c.Categories[0].Sub, 1)
	require.Equal(t, "Movies/HD", c.Categories[0].Sub[0].Name)
}

func TestReleaseToTorznabCopiesTheWireVocabularyVerbatim(t *testing.T) {
	published := metav1.NewTime(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC))
	seeders := int32(12)
	rel := schema.Release{Info: commonv1.ReleaseInfo{
		GUID: "guid-1", Title: "The Matrix", DownloadURL: "https://idx/dl/1",
		InfoURL: "https://idx/details/1", SizeBytes: 1 << 30, PublishedAt: &published,
		Seeders: &seeders, InfoHash: "abc123", MagnetURL: "magnet:?xt=1",
		Categories: []int32{2000, 2040}, IDs: map[string]string{"imdb": "tt0133093"},
	}}

	out := releaseToTorznab(rel)
	require.Equal(t, "guid-1", out.GUID)
	require.Equal(t, "https://idx/dl/1", out.Link)
	require.Equal(t, "https://idx/details/1", out.CommentURL)
	require.EqualValues(t, 1<<30, out.Size)
	require.True(t, out.PubDate.Equal(published.Time))
	require.Equal(t, seeders, *out.Seeders)
	require.Equal(t, "abc123", out.InfoHash)
	require.Equal(t, "magnet:?xt=1", out.MagnetURL)
	require.Len(t, out.Categories, 2)
	require.Equal(t, "tt0133093", out.IDs["imdb"])
}

func TestResolveIndexerWithNamespaceConfiguredIsASingleGet(t *testing.T) {
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "idx1", Namespace: "media"}}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(idx).Build()
	s := &Server{cfg: Config{Client: c, Namespace: "media"}}

	got, err := s.resolveIndexer(context.Background(), "idx1")
	require.NoError(t, err)
	require.Equal(t, "idx1", got.Name)

	_, err = s.resolveIndexer(context.Background(), "missing")
	require.Error(t, err)
}

func TestResolveIndexerWithoutNamespaceFallsBackToAClusterWideList(t *testing.T) {
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "idx1", Namespace: "media"}}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(idx).Build()
	s := &Server{cfg: Config{Client: c}}

	got, err := s.resolveIndexer(context.Background(), "idx1")
	require.NoError(t, err)
	require.Equal(t, "media", got.Namespace)

	_, err = s.resolveIndexer(context.Background(), "missing")
	require.ErrorIs(t, err, errIndexerNotFound)
}

func TestResolveIndexerRefusesAnAmbiguousNameAcrossNamespaces(t *testing.T) {
	a := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "idx1", Namespace: "media"}}
	b := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "idx1", Namespace: "media-staging"}}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(a, b).Build()
	s := &Server{cfg: Config{Client: c}}

	_, err := s.resolveIndexer(context.Background(), "idx1")
	require.Error(t, err)
	require.NotErrorIs(t, err, errIndexerNotFound, "ambiguous is a different failure than absent")
}

func TestIndexerEnabled(t *testing.T) {
	require.True(t, indexerEnabled(&indexv1alpha1.Indexer{}), "nil Enabled defaults to enabled, per the CRD default")
	on, off := true, false
	require.True(t, indexerEnabled(&indexv1alpha1.Indexer{Spec: indexv1alpha1.IndexerSpec{Enabled: &on}}))
	require.False(t, indexerEnabled(&indexv1alpha1.Indexer{Spec: indexv1alpha1.IndexerSpec{Enabled: &off}}))
}
