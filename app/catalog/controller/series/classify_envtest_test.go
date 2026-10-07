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

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// TestSeriesClassifiesAnimeOnce is anime dual-audio spec §4: an anime series
// takes its RootFolder's anime defaults once, under catalogarr-classify,
// records status.classification, keeps the owner's later change, keeps the
// record through an early return, and a folder whose defaults arrive later
// still classifies.
func TestSeriesClassifiesAnimeOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	direct, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
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
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, catalogac.Series(name, ns).WithStatus(
			catalogac.SeriesStatus().WithMetadata(catalogac.SeriesMetadata().WithTitle(name).WithGenres(genres...).
				WithStatus(catalogv1alpha1.SeriesRunStatusEnded).WithRefreshedAt(metav1.Now()).WithSchemaVersion(metadata.SchemaVersion))))
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
			_, _ = r.Reconcile(ctx, req)
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
	classifyOwnsProfile, catalogarrOwnsRecord := false, false
	for _, mf := range got.ManagedFields {
		raw := ""
		if mf.FieldsV1 != nil {
			raw = mf.FieldsV1.GetRawString()
		}
		if mf.Manager == string(k8s.ManagerCatalogarrClassify) && mf.Subresource == "" && strings.Contains(raw, `"f:qualityProfileRef"`) {
			classifyOwnsProfile = true
		}
		if mf.Manager == string(k8s.ManagerCatalogarr) && mf.Subresource == "status" && strings.Contains(raw, `"f:classification"`) {
			catalogarrOwnsRecord = true
		}
	}
	require.True(t, classifyOwnsProfile, "catalogarr-classify must own spec.qualityProfileRef")
	require.True(t, catalogarrOwnsRecord, "catalogarr must own status.classification")

	// The owner's later change sticks.
	require.NoError(t, c.Patch(ctx, &got, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"qualityProfileRef":"web-1080p"}}`)), client.FieldOwner("clustarr-ui")))
	require.Eventually(t, func() bool {
		return c.Get(ctx, naruto.NamespacedName, &s) == nil && s.Spec.QualityProfileRef == "web-1080p"
	}, 5*time.Second, 10*time.Millisecond)
	for range 2 {
		_, err = r.Reconcile(ctx, naruto)
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
		_, _ = r.Reconcile(ctx, late)
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
