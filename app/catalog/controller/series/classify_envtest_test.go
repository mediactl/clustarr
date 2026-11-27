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

package series_test

import (
	"context"
	"strings"
	"testing"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"

	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/test/librarytest"
)

// TestSeriesClassifiesAnimeOnce is anime dual-audio spec §4: an anime series
// takes its RootFolder's anime defaults once, under catalog-classify,
// records status.classification, keeps the owner's later change, keeps the
// record through an early return, and a folder whose defaults arrive later
// still classifies.
func TestSeriesClassifiesAnimeOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	direct, err := librarytest.NewClient(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	const ns = "classify-ns"
	require.NoError(t, c.Create(ctx, testNamespace(ns)))
	rf := testRootFolder(ns, "tv-root", "/data/media/tv")
	rf.Spec.Defaults.Anime = &catalogv1alpha1.AnimeDefaults{QualityProfileRef: "anime-web-1080p"}
	require.NoError(t, c.Create(ctx, rf))
	require.NoError(t, c.Create(ctx, testRootFolder(ns, "plain-root", "/data/media/tv2")))

	r := &series.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: fakePublisher{}, requester: &fakeEpisodeRPC{}},
	}
	newSeries := func(name, root string, genres ...string) reconcile.Request {
		require.NoError(t, c.Create(ctx, &catalogv1alpha1.Series{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 79824, QualityProfileRef: "web-1080p", RootFolderRef: root},
		}))
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogMetadata, catalogac.Series(name, ns).WithStatus(
			catalogac.SeriesStatus().WithMetadata(catalogac.SeriesMetadata().WithTitle(name).WithGenres(genres...).
				WithStatus(catalogv1alpha1.SeriesRunStatusEnded).WithRefreshedAt(metav1.Now()).WithSchemaVersion(metadata.SchemaVersion(commonv1.MediaKindSeries)))))
		require.NoError(t, err)
		key := types.NamespacedName{Namespace: ns, Name: name}
		require.Eventually(t, func() bool {
			var s catalogv1alpha1.Series
			return c.Get(ctx, key, &s) == nil && s.Status.Metadata != nil
		}, 5*time.Second, 10*time.Millisecond)
		return reconcile.Request{NamespacedName: key}
	}
	reconcileUntil := func(req reconcile.Request, what string, ok func(s catalogv1alpha1.Series) bool) catalogv1alpha1.Series {
		var s catalogv1alpha1.Series
		require.Eventually(t, func() bool {
			_, _ = r.ReconcileItem(ctx, req.NamespacedName)
			return c.Get(ctx, req.NamespacedName, &s) == nil && ok(s)
		}, 10*time.Second, 50*time.Millisecond, what)
		return s
	}

	naruto := newSeries("naruto", "tv-root", "Animation", "Anime")
	s := reconcileUntil(naruto, "the anime series was never classified", func(s catalogv1alpha1.Series) bool {
		return s.Status.Classification != nil && s.Spec.QualityProfileRef == "anime-web-1080p"
	})
	require.True(t, s.Status.Classification.Anime)
	require.False(t, s.Status.Classification.AppliedAt.IsZero())
	require.Equal(t, catalogv1alpha1.SeriesTypeAnime, s.Spec.SeriesType)

	// Ownership, through the direct client: caches strip managedFields.
	var got catalogv1alpha1.Series
	require.NoError(t, direct.Get(ctx, naruto.NamespacedName, &got))
	classifyOwnsProfile, catalogOwnsRecord := false, false
	for _, mf := range got.ManagedFields {
		raw := ""
		if mf.FieldsV1 != nil {
			raw = mf.FieldsV1.GetRawString()
		}
		if mf.Manager == string(k8s.ManagerCatalogClassify) && mf.Subresource == "" && strings.Contains(raw, `"f:qualityProfileRef"`) {
			classifyOwnsProfile = true
		}
		if mf.Manager == string(k8s.ManagerCatalog) && mf.Subresource == "status" && strings.Contains(raw, `"f:classification"`) {
			catalogOwnsRecord = true
		}
	}
	require.True(t, classifyOwnsProfile, "catalog-classify must own spec.qualityProfileRef")
	require.True(t, catalogOwnsRecord, "catalog must own status.classification")

	// The owner's later change sticks.
	require.NoError(t, c.Patch(ctx, &got, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"qualityProfileRef":"web-1080p"}}`)), client.FieldOwner("clustarr-ui")))
	require.Eventually(t, func() bool {
		return c.Get(ctx, naruto.NamespacedName, &s) == nil && s.Spec.QualityProfileRef == "web-1080p"
	}, 5*time.Second, 10*time.Millisecond)
	for range 2 {
		_, err = r.ReconcileItem(ctx, naruto.NamespacedName)
		require.NoError(t, err)
	}
	require.NoError(t, direct.Get(ctx, naruto.NamespacedName, &s))
	require.Equal(t, "web-1080p", s.Spec.QualityProfileRef, "a classified series must keep the owner's later profile")

	// A non-anime series is recorded, not moved.
	drama := newSeries("the-wire", "tv-root", "Drama")
	s = reconcileUntil(drama, "the non-anime series was never recorded", func(s catalogv1alpha1.Series) bool { return s.Status.Classification != nil })
	require.False(t, s.Status.Classification.Anime)
	require.Equal(t, "web-1080p", s.Spec.QualityProfileRef)

	// Defaults that arrive later still classify (Review Focus 4).
	late := newSeries("bleach", "plain-root", "Anime")
	for range 2 {
		_, _ = r.ReconcileItem(ctx, late.NamespacedName)
	}
	require.NoError(t, direct.Get(ctx, late.NamespacedName, &s))
	require.Nil(t, s.Status.Classification, "no anime defaults: nothing may be recorded yet")
	var plain catalogv1alpha1.RootFolder
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "plain-root"}, &plain))
	require.NoError(t, c.Patch(ctx, &plain, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"defaults":{"anime":{"qualityProfileRef":"anime-web-1080p"}}}}`))))
	reconcileUntil(late, "setting the defaults later never classified the series", func(s catalogv1alpha1.Series) bool {
		return s.Status.Classification != nil && s.Status.Classification.Anime && s.Spec.QualityProfileRef == "anime-web-1080p"
	})

	// An early return keeps the record (Review Focus 5).
	require.NoError(t, c.Delete(ctx, rf))
	require.Eventually(t, func() bool {
		var gone catalogv1alpha1.RootFolder
		return c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "tv-root"}, &gone) != nil
	}, 5*time.Second, 10*time.Millisecond)
	s = reconcileUntil(naruto, "the RootFolderNotFound path never ran", func(s catalogv1alpha1.Series) bool {
		for _, cond := range s.Status.Conditions {
			if cond.Type == k8s.ConditionReady && cond.Reason == "RootFolderNotFound" {
				return true
			}
		}
		return false
	})
	require.NotNil(t, s.Status.Classification, "an early return must not release status.classification")
	require.True(t, s.Status.Classification.Anime)
}

// TestAnAnimeIsRoutedToTheAnimeRoot is the anime library design
// (2026-11-25) §2: an anime added under the TV root -- from a watchlist, by
// hand -- takes the anime root with its profile and type, in the one
// classification, before it has an Episode. While the folder its root's
// anime defaults name is missing it is held, unclassified and without
// Episodes, so nothing is searched or imported under the TV root; the
// folder's arrival wakes it. Anything that is not anime stays where it is.
func TestAnAnimeIsRoutedToTheAnimeRoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	const ns = "route-ns"
	require.NoError(t, c.Create(ctx, testNamespace(ns)))
	tv := testRootFolder(ns, "tv", "/data/media/tv")
	tv.Spec.Defaults.Anime = &catalogv1alpha1.AnimeDefaults{QualityProfileRef: "anime-web-1080p", RootFolderRef: "anime"}
	require.NoError(t, c.Create(ctx, tv))

	rpc := &fakeEpisodeRPC{episodes: []metadata.Episode{{SeasonNumber: 1, EpisodeNumber: 1, Title: "The Journey's End"}}}
	r := &series.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: fakePublisher{}, requester: rpc},
	}
	newSeries := func(name string, genres ...string) types.NamespacedName {
		require.NoError(t, c.Create(ctx, &catalogv1alpha1.Series{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 79824, QualityProfileRef: "web-1080p", RootFolderRef: "tv"},
		}))
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogMetadata, catalogac.Series(name, ns).WithStatus(
			catalogac.SeriesStatus().WithMetadata(catalogac.SeriesMetadata().WithTitle(name).WithGenres(genres...).
				WithStatus(catalogv1alpha1.SeriesRunStatusEnded).WithRefreshedAt(metav1.Now()).WithSchemaVersion(metadata.SchemaVersion(commonv1.MediaKindSeries)))))
		require.NoError(t, err)
		key := types.NamespacedName{Namespace: ns, Name: name}
		require.Eventually(t, func() bool {
			var s catalogv1alpha1.Series
			return c.Get(ctx, key, &s) == nil && s.Status.Metadata != nil
		}, 5*time.Second, 10*time.Millisecond)
		return key
	}
	reconcileUntil := func(key types.NamespacedName, what string, ok func(s catalogv1alpha1.Series) bool) catalogv1alpha1.Series {
		var s catalogv1alpha1.Series
		require.Eventually(t, func() bool {
			_, _ = r.ReconcileItem(ctx, key)
			return c.Get(ctx, key, &s) == nil && ok(s)
		}, 10*time.Second, 50*time.Millisecond, what)
		return s
	}
	episodes := func(name string) int {
		var eps catalogv1alpha1.EpisodeList
		require.NoError(t, c.List(ctx, &eps, client.InNamespace(ns)))
		n := 0
		for i := range eps.Items {
			if eps.Items[i].Spec.SeriesRef == name {
				n++
			}
		}
		return n
	}
	held := func(s catalogv1alpha1.Series) bool {
		ready := apimeta.FindStatusCondition(s.Status.Conditions, k8s.ConditionReady)
		return ready != nil && ready.Status == metav1.ConditionFalse && ready.Reason == series.ReasonAnimeRootFolderNotFound
	}

	// No anime root yet: held.
	frieren := newSeries("frieren", "Animation", "Anime")
	s := reconcileUntil(frieren, "the anime was never held for its root folder", held)
	require.Nil(t, s.Status.Classification, "recorded without the move, it would stay in TV for good")
	require.Equal(t, "tv", s.Spec.RootFolderRef)
	require.Equal(t, "web-1080p", s.Spec.QualityProfileRef)
	require.Zero(t, episodes("frieren"), "a held anime has no Episode to search under the TV root")

	// A folder that is there but is no anime root holds it too.
	wrong := testRootFolder(ns, "anime", "/data/media/anime")
	require.NoError(t, c.Create(ctx, wrong))
	for range 2 {
		_, _ = r.ReconcileItem(ctx, frieren)
	}
	require.NoError(t, c.Get(ctx, frieren, &s))
	require.True(t, held(s))
	require.Nil(t, s.Status.Classification)

	// It becomes one, and that wakes the series waiting for it.
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(wrong), wrong))
	wrong.Spec.Defaults.SeriesType = catalogv1alpha1.SeriesTypeAnime
	require.NoError(t, c.Update(ctx, wrong))
	require.Eventually(t, func() bool {
		var rf catalogv1alpha1.RootFolder
		return c.Get(ctx, client.ObjectKeyFromObject(wrong), &rf) == nil && rf.Spec.AnimeRoot()
	}, 5*time.Second, 10*time.Millisecond)
	woken := series.MapRootFolder(r, ctx, wrong)
	require.Contains(t, woken, reconcile.Request{NamespacedName: frieren}, "the anime root's change wakes the series its TV root routes there")

	s = reconcileUntil(frieren, "the anime was never routed", func(s catalogv1alpha1.Series) bool {
		return s.Status.Classification != nil && s.Spec.RootFolderRef == "anime"
	})
	require.True(t, s.Status.Classification.Anime)
	require.Equal(t, "anime-web-1080p", s.Spec.QualityProfileRef)
	require.Equal(t, catalogv1alpha1.SeriesTypeAnime, s.Spec.SeriesType)
	s = reconcileUntil(frieren, "the routed anime never took a folder under the anime root", func(s catalogv1alpha1.Series) bool {
		return strings.HasPrefix(s.Status.Path, "/data/media/anime/") && !held(s)
	})
	require.Positive(t, episodes("frieren"), "once routed it fans out as any series does")

	// Anything else stays in TV.
	wire := newSeries("the-wire", "Drama")
	s = reconcileUntil(wire, "the drama was never recorded", func(s catalogv1alpha1.Series) bool { return s.Status.Classification != nil })
	require.False(t, s.Status.Classification.Anime)
	require.Equal(t, "tv", s.Spec.RootFolderRef)
	s = reconcileUntil(wire, "the drama never took a folder", func(s catalogv1alpha1.Series) bool { return s.Status.Path != "" })
	require.True(t, strings.HasPrefix(s.Status.Path, "/data/media/tv/"), s.Status.Path)

	// The classification's spec patch and its status record are two writes.
	// A pass that reads the series between them finds it under the anime
	// root with nothing recorded -- the state built here, which is also an
	// anime a list created under the anime root -- and records it anime by
	// the folder it is under, patching nothing. (The first cut found no
	// anime defaults there, recorded nothing, and so released the record
	// the first write's pass had made.)
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "between-writes", Namespace: ns},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID: 79824, QualityProfileRef: "anime-web-1080p", RootFolderRef: "anime",
			SeriesType: catalogv1alpha1.SeriesTypeAnime,
		},
	}))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogMetadata, catalogac.Series("between-writes", ns).WithStatus(
		catalogac.SeriesStatus().WithMetadata(catalogac.SeriesMetadata().WithTitle("between-writes").WithGenres("Drama").
			WithStatus(catalogv1alpha1.SeriesRunStatusEnded).WithRefreshedAt(metav1.Now()).WithSchemaVersion(metadata.SchemaVersion(commonv1.MediaKindSeries)))))
	require.NoError(t, err)
	between := types.NamespacedName{Namespace: ns, Name: "between-writes"}
	s = reconcileUntil(between, "a series under the anime root was never recorded", func(s catalogv1alpha1.Series) bool {
		return s.Status.Classification != nil
	})
	require.True(t, s.Status.Classification.Anime, "under an anime root a series is anime, whatever its genres")
	require.Empty(t, s.Status.Classification.QualityProfileRef, "nothing was applied")
	require.Equal(t, "anime", s.Spec.RootFolderRef)
	for range 3 {
		_, _ = r.ReconcileItem(ctx, between)
	}
	require.NoError(t, c.Get(ctx, between, &s))
	require.NotNil(t, s.Status.Classification, "and it is kept")

	// The apiserver holds the field to a series root, and off itself.
	films := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "films", Namespace: ns},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: "/data/media/movies", Kind: catalogv1alpha1.RootFolderKindMovie,
			Defaults: catalogv1alpha1.RootDefaults{Anime: &catalogv1alpha1.AnimeDefaults{QualityProfileRef: "p", RootFolderRef: "anime"}},
		},
	}
	require.ErrorContains(t, c.Create(ctx, films), "an anime film stays under its movie root")
	self := testRootFolder(ns, "loop", "/data/media/loop")
	self.Spec.Defaults.Anime = &catalogv1alpha1.AnimeDefaults{QualityProfileRef: "p", RootFolderRef: "loop"}
	require.ErrorContains(t, c.Create(ctx, self), "names another root folder")
}
