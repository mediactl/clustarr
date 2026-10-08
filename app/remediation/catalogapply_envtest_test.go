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

package remediation_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/album"
	"github.com/mediactl/clustarr/app/catalog/controller/audiobook"
	"github.com/mediactl/clustarr/app/catalog/controller/book"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/app/catalog/controller/movie"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/itemtest"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// fullQueueBus is the Env's membus whose publishes fail ErrQueueFull while
// full is set: the item kinds' QueueFull early return.
type fullQueueBus struct {
	*membus.Bus
	full atomic.Bool
}

func (b *fullQueueBus) Publish(ctx context.Context, subject string, e *events.Envelope, opts ...events.PublishOption) (events.Receipt, error) {
	if b.full.Load() {
		return events.Receipt{}, events.ErrQueueFull
	}
	return b.Bus.Publish(ctx, subject, e, opts...)
}

// ownerKind is one of the six kinds that own grab entries in status.downloads.
type ownerKind struct {
	kind remediation.KeyKind
	obj  func(ns, name string) client.Object
	item func(c client.Client, s *runtime.Scheme, bus *fullQueueBus) rollup.Item
}

func ownerKinds() []ownerKind {
	rec := func() *k8sevents.FakeRecorder { return k8sevents.NewFakeRecorder(1024) }
	return []ownerKind{
		{remediation.KindMovie, func(ns, name string) client.Object {
			return &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
				Spec: catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd", RootFolderRef: "movies"}}
		}, func(c client.Client, s *runtime.Scheme, bus *fullQueueBus) rollup.Item {
			return &movie.Reconciler{Client: c, Scheme: s, Recorder: rec(), Bus: bus}
		}},
		{remediation.KindSeries, func(ns, name string) client.Object {
			return &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
				Spec: catalogv1alpha1.SeriesSpec{TvdbID: 81189, QualityProfileRef: "hd", RootFolderRef: "tv"}}
		}, func(c client.Client, s *runtime.Scheme, bus *fullQueueBus) rollup.Item {
			return &series.Reconciler{Client: c, Scheme: s, Recorder: rec(), Bus: bus}
		}},
		{remediation.KindAlbum, func(ns, name string) client.Object {
			return &catalogv1alpha1.Album{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
				Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "b1392450-e666-3926-a536-22c65f834433"}}
		}, func(c client.Client, s *runtime.Scheme, bus *fullQueueBus) rollup.Item {
			return &album.Reconciler{Client: c, Scheme: s, Recorder: rec(), Bus: bus, Releases: album.NewReleaseCache(album.DefaultReleaseCacheSize)}
		}},
		{remediation.KindBook, func(ns, name string) client.Object {
			return &catalogv1alpha1.Book{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
				Spec: catalogv1alpha1.BookSpec{WorkID: "OL45883W"}}
		}, func(c client.Client, s *runtime.Scheme, bus *fullQueueBus) rollup.Item {
			return &book.Reconciler{Client: c, Scheme: s, Recorder: rec(), Bus: bus}
		}},
		{remediation.KindAudiobook, func(ns, name string) client.Object {
			return &catalogv1alpha1.Audiobook{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
				Spec: catalogv1alpha1.AudiobookSpec{ASIN: "B002V0QK4C", QualityProfileRef: "audio", RootFolderRef: "audiobooks"}}
		}, func(c client.Client, s *runtime.Scheme, bus *fullQueueBus) rollup.Item {
			return &audiobook.Reconciler{Client: c, Scheme: s, Recorder: rec(), Bus: bus}
		}},
		{remediation.KindComic, func(ns, name string) client.Object {
			return &catalogv1alpha1.Comic{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
				Spec: catalogv1alpha1.ComicSpec{Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-18166", QualityProfileRef: "comics", RootFolderRef: "comics"}}
		}, func(c client.Client, s *runtime.Scheme, bus *fullQueueBus) rollup.Item {
			return &comic.Reconciler{Client: c, Scheme: s, Recorder: rec(), Bus: bus}
		}},
	}
}

// downloadsOf is obj's stored status.downloads, read through Admin.
func downloadsOf(t *testing.T, e *remediationtest.Env, obj client.Object) ([]any, []metav1.ManagedFieldsEntry) {
	t.Helper()
	fresh := obj.DeepCopyObject().(client.Object)
	require.NoError(t, e.Admin.Get(t.Context(), client.ObjectKeyFromObject(obj), fresh))
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(fresh)
	require.NoError(t, err)
	list, _, err := unstructured.NestedSlice(u, "status", "downloads")
	require.NoError(t, err)
	return list, fresh.GetManagedFields()
}

func appliesTo(e *remediationtest.Env, kind remediation.KeyKind, name string) int {
	n := 0
	for _, a := range e.StatusApplies() {
		md, _ := a["metadata"].(map[string]any)
		if a["kind"] == string(kind) && md["name"] == name {
			n++
		}
	}
	return n
}

// TestEveryCatalogApplySendsTheDownloads (ADR-0019 §7.0, Review Focus 4):
// the catalog apply of each of the six kinds that own grab entries re-sends
// the stored status.downloads on every apply site -- the main path, the
// QueueFull and RootFolderNotFound early returns and the deletion path --
// whether or not a stage contributed, so an item in its steady state with
// entries keeps them, under catalog, through each.
func TestEveryCatalogApplySendsTheDownloads(t *testing.T) {
	e := remediationtest.Start(t)
	bus := &fullQueueBus{Bus: e.Bus}
	e.Loop.Bus = bus
	scheme := e.Admin.Scheme()
	for _, ok := range ownerKinds() {
		t.Run(string(ok.kind), func(t *testing.T) {
			ns := "dl-" + strings.ToLower(string(ok.kind))
			remediationtest.MustNamespace(t, t.Context(), e.Admin, ns)
			obj := ok.obj(ns, "owner")
			require.NoError(t, e.Admin.Create(t.Context(), obj))
			var contribution atomic.Pointer[itempass.Contribution]
			stage := &stubStage{name: remediation.StageDownloads, plan: func(*remediation.ItemView) (remediation.ItemResult, error) {
				if c := contribution.Load(); c != nil {
					return remediation.ItemResult{Contribution: *c}, nil
				}
				return remediation.ItemResult{}, nil
			}}
			ir := &remediation.ItemReconciler{
				Items:  map[remediation.KeyKind]rollup.Item{ok.kind: ok.item(e.Client(), scheme, bus)},
				Stages: []remediation.ItemStage{stage}, Env: e.Loop, Client: e.Client(),
			}
			pass := func(t *testing.T, what string) {
				t.Helper()
				before := appliesTo(e, ok.kind, "owner")
				_, err := ir.Reconcile(t.Context(), remediation.Key{Kind: ok.kind, Namespace: ns, Name: "owner"})
				require.NoError(t, err, what)
				require.Greater(t, appliesTo(e, ok.kind, "owner"), before, "%s: the pass made no catalog apply", what)
			}

			// The steady state: the downloads stage's entries, applied by the kind.
			entries := []catalogv1alpha1.DownloadEntry{itemtest.Entry("e1", commonv1.DownloadPhaseDownloading)}
			contribution.Store(&itempass.Contribution{Downloads: &entries})
			pass(t, "seed")
			steady, mf := downloadsOf(t, e, obj)
			require.Len(t, steady, 1, "the kind did not apply the stage's entries")
			require.True(t, owns(mf, "catalog", "downloads"), "setup: catalog owns the entries")
			contribution.Store(nil)

			check := func(t *testing.T, what string) {
				t.Helper()
				got, mf := downloadsOf(t, e, obj)
				assert.Equal(t, steady, got, "%s changed the stored entries", what)
				assert.True(t, owns(mf, "catalog", "downloads"), "%s released status.downloads", what)
			}
			pass(t, "the main path, no contribution")
			check(t, "the main path")

			bus.full.Store(true)
			pass(t, "QueueFull")
			bus.full.Store(false)
			check(t, "the QueueFull early return")

			// Fresh metadata from the gateway's manager: the pass reaches the
			// owner's RootFolder (or Artist) lookup, which finds none.
			u := &unstructured.Unstructured{}
			gvk, err := apiutil.GVKForObject(obj, scheme)
			require.NoError(t, err)
			u.SetGroupVersionKind(gvk)
			u.SetNamespace(ns)
			u.SetName("owner")
			require.NoError(t, unstructured.SetNestedMap(u.Object, map[string]any{
				"title": "Owner", "refreshedAt": time.Now().UTC().Format(time.RFC3339),
			}, "status", "metadata"))
			_, err = k8s.PatchStatusUnstructured(t.Context(), e.Admin, k8s.ManagerCatalogMetadata, u)
			require.NoError(t, err)
			pass(t, "a missing RootFolder")
			check(t, "the RootFolderNotFound early return")

			// The deletion path: the transfers finalizer holds the owner while
			// the downloads stage decides the entries' removal.
			patch := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"finalizers":["`+catalogv1alpha1.FinalizerTransfers+`"]}}`))
			require.NoError(t, e.Admin.Patch(t.Context(), obj.DeepCopyObject().(client.Object), patch))
			require.NoError(t, e.Admin.Delete(t.Context(), obj.DeepCopyObject().(client.Object)))
			phase := commonv1.DownloadPhaseDownloading
			contribution.Store(&itempass.Contribution{DownloadPhase: &phase})
			pass(t, "deleting")
			check(t, "the deletion path")
		})
	}
}
