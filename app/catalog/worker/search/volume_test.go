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
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A season task names a Series or, since 2026-11-10 (design D10), a Comic
// -- its volume; a container task without a season, or any other kind, is
// no season search.
func TestSeasonSearchableTakesASeriesSeasonOrAComicVolume(t *testing.T) {
	one := int32(1)
	for _, tc := range []struct {
		name string
		task schema.SearchTask
		want bool
	}{
		{"series season", schema.SearchTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindSeries}, Season: &one}, true},
		{"comic volume", schema.SearchTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindComic}, Season: &one}, true},
		{"series without a season", schema.SearchTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindSeries}}, false},
		{"comic without a season", schema.SearchTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindComic}}, false},
		{"an issue is searched itself", schema.SearchTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindIssue}, Season: &one}, false},
		{"an episode is searched itself", schema.SearchTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode}, Season: &one}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, SeasonSearchable(tc.task))
		})
	}
}

// TestAComicVolumeIsQueriedByItsTitle is design D10 on the agent side: the
// manager's season task for a Comic snapshots the volume -- the Comic's
// title and start year as the request's ids, no issue number, the comics
// category, the Comic's profile and the Comic as the owner -- and judges the
// answer as a season search of the comic's titles with no issue and no
// year, since a volume runs for years and takes any issue of the series.
func TestAComicVolumeIsQueriedByItsTitle(t *testing.T) {
	const ns = "media"
	co := &catalogv1alpha1.Comic{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "saga", UID: "uid-saga"},
		Spec: catalogv1alpha1.ComicSpec{
			Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-1", Monitored: ptr.To(true),
			QualityProfileRef: "comic", RootFolderRef: "comics",
		},
		Status: catalogv1alpha1.ComicStatus{Metadata: &catalogv1alpha1.ComicMetadata{
			Title: "Saga", Year: 2012, AlternateTitles: []string{"Saga (2012)"},
		}},
	}
	w := &Worker{Client: fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(co).Build()}
	ref := commonv1.MediaRef{Kind: commonv1.MediaKindComic, Name: "saga"}
	snap, err := w.snapshotSeason(context.Background(), ns, ref, 1)
	require.NoError(t, err)

	require.Equal(t, "Saga", snap.IDs.Title)
	require.EqualValues(t, 2012, snap.IDs.Year)
	require.Empty(t, snap.IDs.Issue, "a volume names no issue")
	require.Equal(t, "comic", snap.QualityProfileRef)
	require.Equal(t, commonv1.MediaKindComic, snap.Target.Kind)
	require.True(t, snap.Target.Monitored)
	require.True(t, snap.Target.Available)
	require.True(t, snap.Target.Identity.SeasonSearch, "any issue of the series is the volume's")
	require.Equal(t, []string{"Saga", "Saga (2012)"}, snap.Target.Identity.Titles)
	require.Empty(t, snap.Target.Identity.Issue)
	require.Zero(t, snap.Target.Identity.Year, "a volume runs for years")
	require.Equal(t, schema.ItemRef{Kind: "comic", Ref: schema.Ref{Namespace: ns, Name: "saga", UID: "uid-saga"}}, snap.Owner,
		"the owner as every snapshot names it, by media kind")

	season := int32(1)
	task := schema.SearchTask{
		MediaRef: ref, Reason: schema.SearchReasonSeason, Season: &season,
		Item:      schema.ItemRef{Kind: "Comic", Ref: schema.Ref{Namespace: ns, Name: "saga", UID: "uid-saga"}},
		Protocols: []commonv1.Protocol{commonv1.ProtocolUsenet},
	}
	req := w.buildRequest(ns, task, snap, nil)
	require.Equal(t, commonv1.MediaKindIssue, req.Kind, "a volume is a comics search")
	require.Equal(t, "Saga", req.Text, "the title alone: no issue number, no season token")
	require.Equal(t, []int32{7030}, req.Categories)
	require.Nil(t, req.Season)
	require.Nil(t, req.Episode)
	require.Empty(t, req.IDs, "no id an indexer takes")
	require.EqualValues(t, 2012, req.Year)
	require.Equal(t, schema.BlockScopeOf(snap.Owner), req.Scope)
	require.Equal(t, []commonv1.Protocol{commonv1.ProtocolUsenet}, req.Protocols)

	t.Run("an unmonitored comic, no metadata", func(t *testing.T) {
		bare := co.DeepCopy()
		bare.Name, bare.Spec.Monitored, bare.Status.Metadata = "bare", ptr.To(false), nil
		w := &Worker{Client: fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(bare).Build()}
		snap, err := w.snapshotSeason(context.Background(), ns, commonv1.MediaRef{Kind: commonv1.MediaKindComic, Name: "bare"}, 1)
		require.NoError(t, err)
		require.False(t, snap.Target.Monitored)
		require.Empty(t, snap.IDs.Title, "no title to query by until metadata lands")
		require.Empty(t, w.buildRequest(ns, task, snap, nil).Text)
	})
}
