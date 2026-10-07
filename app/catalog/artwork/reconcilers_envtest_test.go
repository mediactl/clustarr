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

package artwork_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/album"
	"github.com/mediactl/clustarr/app/catalog/controller/artist"
	"github.com/mediactl/clustarr/app/catalog/controller/audiobook"
	"github.com/mediactl/clustarr/app/catalog/controller/author"
	"github.com/mediactl/clustarr/app/catalog/controller/book"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/app/catalog/controller/movie"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestEveryItemReconcilerPublishesOneFetchOnDrift proves each of the eight
// reconcilers calls artwork.PublishFetch (the helper itself is tested once,
// in drift_test.go): an item created with a spec.artwork override has
// drifted from its empty status.artwork, and reconciling it -- twice, as a
// hot loop would -- puts exactly one ArtworkFetchTask on its subject.
//
// Reconcile is called directly against a plain client rather than through
// eight managers: the drift check sits before reconcileNormal, so what
// reconcileNormal later makes of an item with no RootFolder or profile is
// irrelevant here, and its error is not asserted.
func TestEveryItemReconcilerPublishesOneFetchOnDrift(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	got := collectFetches(t, ctx, bus)

	scheme := k8s.MustNewScheme()
	rec := &k8sevents.FakeRecorder{} // nil Events: records nothing, never blocks
	art := []catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypePoster, URL: "https://example.org/mine.jpg"}}
	meta := func(ns string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: "item", Namespace: ns} }

	for _, tc := range []struct {
		kind commonv1.MediaKind
		obj  func(ns string) client.Object
		r    reconcile.Reconciler
	}{
		{
			kind: commonv1.MediaKindMovie,
			obj: func(ns string) client.Object {
				return &catalogv1alpha1.Movie{ObjectMeta: meta(ns), Spec: catalogv1alpha1.MovieSpec{
					TmdbID: 603, QualityProfileRef: "q", RootFolderRef: "r", Artwork: art,
				}}
			},
			r: &movie.Reconciler{Client: c, Scheme: scheme, Recorder: rec, Bus: bus},
		},
		{
			kind: commonv1.MediaKindSeries,
			obj: func(ns string) client.Object {
				return &catalogv1alpha1.Series{ObjectMeta: meta(ns), Spec: catalogv1alpha1.SeriesSpec{
					TvdbID: 81189, QualityProfileRef: "q", RootFolderRef: "r", Artwork: art,
				}}
			},
			r: &series.Reconciler{Client: c, Scheme: scheme, Recorder: rec, Bus: bus},
		},
		{
			kind: commonv1.MediaKindArtist,
			obj: func(ns string) client.Object {
				return &catalogv1alpha1.Artist{ObjectMeta: meta(ns), Spec: catalogv1alpha1.ArtistSpec{
					MusicBrainzID: "a74b1b7f-71a5-4011-9441-d0b5e4122711", QualityProfileRef: "q", RootFolderRef: "r", Artwork: art,
				}}
			},
			r: &artist.Reconciler{Client: c, Scheme: scheme, Recorder: rec, Bus: bus},
		},
		{
			kind: commonv1.MediaKindAlbum,
			obj: func(ns string) client.Object {
				return &catalogv1alpha1.Album{ObjectMeta: meta(ns), Spec: catalogv1alpha1.AlbumSpec{
					ArtistRef: "radiohead", ReleaseGroupID: "b1392450-e666-3926-a536-22c65f834433", Artwork: art,
				}}
			},
			r: &album.Reconciler{Client: c, Scheme: scheme, Recorder: rec, Bus: bus},
		},
		{
			kind: commonv1.MediaKindAuthor,
			obj: func(ns string) client.Object {
				return &catalogv1alpha1.Author{ObjectMeta: meta(ns), Spec: catalogv1alpha1.AuthorSpec{
					OpenLibraryID: "OL23919A", QualityProfileRef: "q", RootFolderRef: "r", Artwork: art,
				}}
			},
			r: &author.Reconciler{Client: c, Scheme: scheme, Recorder: rec, Bus: bus},
		},
		{
			kind: commonv1.MediaKindBook,
			obj: func(ns string) client.Object {
				return &catalogv1alpha1.Book{ObjectMeta: meta(ns), Spec: catalogv1alpha1.BookSpec{
					WorkID: "OL82563W", Artwork: art,
				}}
			},
			r: &book.Reconciler{Client: c, Scheme: scheme, Recorder: rec, Bus: bus},
		},
		{
			kind: commonv1.MediaKindAudiobook,
			obj: func(ns string) client.Object {
				return &catalogv1alpha1.Audiobook{ObjectMeta: meta(ns), Spec: catalogv1alpha1.AudiobookSpec{
					ASIN: "B017V4IM1G", QualityProfileRef: "q", RootFolderRef: "r", Artwork: art,
				}}
			},
			r: &audiobook.Reconciler{Client: c, Scheme: scheme, Recorder: rec, Bus: bus},
		},
		{
			kind: commonv1.MediaKindComic,
			obj: func(ns string) client.Object {
				return &catalogv1alpha1.Comic{ObjectMeta: meta(ns), Spec: catalogv1alpha1.ComicSpec{
					Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-18257",
					QualityProfileRef: "q", RootFolderRef: "r", Artwork: art,
				}}
			},
			r: &comic.Reconciler{Client: c, Scheme: scheme, Recorder: rec, Bus: bus},
		},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			ns := "drift-" + string(tc.kind)
			require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
			obj := tc.obj(ns)
			require.NoError(t, c.Create(ctx, obj))

			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(obj)}
			for range 2 {
				_, _ = tc.r.Reconcile(ctx, req)
			}

			subject := events.WorkArtworkFetchSubject(events.MediaKey(string(tc.kind), ns, "item"))
			require.Eventually(t, func() bool { return len(got.on(subject)) >= 1 }, 2*time.Second, 5*time.Millisecond,
				"%s's reconciler never published the fetch task", tc.kind)
			time.Sleep(30 * time.Millisecond)
			assert.Len(t, got.on(subject), 1, "one task per spec.artwork, however often the item reconciles")
		})
	}
}
