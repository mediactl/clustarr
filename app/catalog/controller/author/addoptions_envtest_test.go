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

package author_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/author"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// The first fan-out applies the add-time monitor mode though it runs a pass
// after the Author was first reconciled (Readarr's addOptions.monitor;
// design D2): a pass without metadata flips status.addOptionsApplied for
// nothing, since DesiredBooks reads it to choose between the add-time mode
// and spec.monitorNewItems. Until 2026-11-10 every pass sent the flag true,
// so once the fan-out waited for the document the first real one ran with it
// flipped: under none, future, missing or existing every book started
// monitored, spec.monitorNewItems defaulting to all. And with the flag false
// through the MetadataPending pass, the Author is announced added off
// observedGeneration, once, not on each such pass.
func TestTheFirstBookFanOutAppliesTheAddTimeMonitorMode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	const ns = "author-addoptions-ns"
	require.NoError(t, c.Create(ctx, testNamespace(ns)))
	createRootFolder(t, ctx, c, ns, "book-root", "/data/media/books")

	a := &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "tolkien", Namespace: ns},
		Spec: catalogv1alpha1.AuthorSpec{
			OpenLibraryID: "OL23919A", QualityProfileRef: "none", RootFolderRef: "book-root",
			AddOptions: catalogv1alpha1.AuthorAddOptions{Monitor: catalogv1alpha1.AuthorMonitorNone},
		},
	}
	require.NoError(t, c.Create(ctx, a))
	key := client.ObjectKeyFromObject(a)
	require.Eventually(t, func() bool { return c.Get(ctx, key, &catalogv1alpha1.Author{}) == nil }, 5*time.Second, 10*time.Millisecond)

	published := time.Date(1937, 9, 21, 0, 0, 0, 0, time.UTC)
	rpc := &countingBookRPC{books: []metadata.Book{
		{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL45883W"}, Title: "The Hobbit", FirstPublished: &published},
	}}
	pub := &itemRecorder{}
	r := &author.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: combinedBus{Publisher: pub, requester: rpc}}

	// The metadata-less pass: no list fetched, and the flag not flipped.
	_, err := r.ReconcileItem(ctx, key)
	require.NoError(t, err)
	assert.Zero(t, rpc.calls.Load())
	var got catalogv1alpha1.Author
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && got.Status.ObservedGeneration == got.Generation
	}, 5*time.Second, 10*time.Millisecond, "the pass never wrote its status")
	assert.False(t, got.Status.AddOptionsApplied, "no fan-out has run: the add-time monitor mode is still owed")
	assert.Equal(t, catalogv1alpha1.MonitorNewChildrenAll, got.Spec.MonitorNewItems, "the CRD default a flipped flag would create the book under")

	// The document, as the loop's metadata stage writes it, then the pass
	// its write wakes: the one fetch, the Book under the add-time mode, and
	// the flag recorded with that first complete fan-out.
	metaAC := catalogac.Author(a.Name, a.Namespace).WithStatus(catalogac.AuthorStatus().WithMetadata(
		catalogac.AuthorMetadata().WithName("J. R. R. Tolkien").WithRefreshedAt(metav1.Now()).WithSchemaVersion(metadata.SchemaVersion)))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogMetadata, metaAC)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && got.Status.Metadata != nil
	}, 5*time.Second, 10*time.Millisecond, "the metadata document never reached the cache")
	_, err = r.ReconcileItem(ctx, key)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rpc.calls.Load())
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && got.Status.AddOptionsApplied
	}, 5*time.Second, 10*time.Millisecond, "the first complete fan-out records addOptionsApplied")

	var bk catalogv1alpha1.Book
	bookKey := types.NamespacedName{Namespace: ns, Name: author.BookName(a.Name, "OL45883W")}
	require.Eventually(t, func() bool { return c.Get(ctx, bookKey, &bk) == nil }, 5*time.Second, 10*time.Millisecond, "the fan-out never created the Book")
	require.NotNil(t, bk.Spec.Monitored, "the first fan-out sets spec.monitored on every Book it creates")
	assert.False(t, *bk.Spec.Monitored, "none: the book starts unmonitored, whatever spec.monitorNewItems says")
	assert.Equal(t, 1, pub.count(), "announced added once, on the first pass, not again while the flag waits for the fan-out")
}
