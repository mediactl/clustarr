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

package readwrite_test

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/library/readwrite"
	"github.com/mediactl/clustarr/test/librarytest"
)

// TestTheCollectorDeletesWhatItsOwnerLeftBehind is design §3.4: a Comic is
// a custom resource and its Issues are rows, so the apiserver's garbage
// collector no longer deletes an Issue with its Comic. The collector does,
// by that collector's rule. An Issue whose Comic is there is kept; with the
// Comic deleted it is deleted, held by its finalizer as a delete of it is;
// one whose Comic was made again under the same name belongs to the Comic
// that is gone and goes too; one with no owner is nobody's to collect.
func TestTheCollectorDeletesWhatItsOwnerLeftBehind(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if os.Getenv("CLUSTARR_REQUIRE_TOOLS") == "1" {
			t.Fatal("KUBEBUILDER_ASSETS is unset and CLUSTARR_REQUIRE_TOOLS=1")
		}
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	st := librarytest.Start(t).Store(t, nil)
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, env.Stop()) })
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1.AddToScheme(scheme))
	api, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	c := readwrite.NewClient(api, st, false, "clustarr")
	ctx := t.Context()

	comic := func(name string) *catalogv1.Comic {
		return &catalogv1.Comic{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: catalogv1.ComicSpec{
				Source: catalogv1.ComicSourceComicVine, SourceID: "4050-" + name,
				QualityProfileRef: "any", RootFolderRef: "comics",
			},
		}
	}
	issueOf := func(owner *catalogv1.Comic, name string, finalizer bool) *catalogv1.Issue {
		iss := &catalogv1.Issue{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       catalogv1.IssueSpec{ComicRef: "x", Number: "1", CalculatedNumberCentis: 100},
		}
		if owner != nil {
			require.NoError(t, controllerutil.SetControllerReference(owner, iss, scheme))
		}
		if finalizer {
			controllerutil.AddFinalizer(iss, "clustarr.io/item")
		}
		require.NoError(t, c.Create(ctx, iss))
		return iss
	}
	exists := func(name string) (*catalogv1.Issue, bool) {
		var iss catalogv1.Issue
		err := c.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, &iss)
		if apierrors.IsNotFound(err) {
			return nil, false
		}
		require.NoError(t, err)
		return &iss, true
	}

	saga, girls := comic("saga"), comic("paper-girls")
	require.NoError(t, c.Create(ctx, saga))
	require.NoError(t, c.Create(ctx, girls))
	issueOf(saga, "saga-1", false)
	issueOf(saga, "saga-2", true)
	issueOf(girls, "paper-girls-1", false)
	issueOf(nil, "loose-1", false)

	collector := &readwrite.Collector{Store: st, Owners: api}
	n, err := collector.Sweep(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "every owner is there")

	t.Log("saga is deleted, and made again under its name")
	require.NoError(t, c.Delete(ctx, saga))
	again := comic("saga")
	require.NoError(t, c.Create(ctx, again))
	require.NotEqual(t, saga.UID, again.UID)
	issueOf(again, "saga-3", false)

	n, err = collector.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "the two Issues of the Comic that is gone")
	_, ok := exists("saga-1")
	assert.False(t, ok, "an Issue with no finalizer is gone")
	held, ok := exists("saga-2")
	require.True(t, ok, "an Issue with a finalizer is marked, and waits for it")
	assert.NotNil(t, held.DeletionTimestamp)
	for _, name := range []string{"saga-3", "paper-girls-1", "loose-1"} {
		iss, ok := exists(name)
		require.True(t, ok, name)
		assert.Nil(t, iss.DeletionTimestamp, name)
	}

	t.Log("a second sweep finds the marked Issue already going and nothing else")
	n, err = collector.Sweep(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "the marked Issue is already going")
	held, ok = exists("saga-2")
	require.True(t, ok)
	controllerutil.RemoveFinalizer(held, "clustarr.io/item")
	require.NoError(t, c.Update(ctx, held))
	_, ok = exists("saga-2")
	assert.False(t, ok, "its finalizer removed, it is gone")
}

// countingReader counts the Gets sent through it.
type countingReader struct {
	client.Reader
	gets atomic.Int64
}

func (r *countingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	r.gets.Add(1)
	return r.Reader.Get(ctx, key, obj, opts...)
}

// staleReader is a cache that has not seen a delete: it still answers with
// held for held's name.
type staleReader struct {
	client.Reader
	held *catalogv1.Comic
}

func (r *staleReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c, ok := obj.(*catalogv1.Comic); ok && key.Name == r.held.Name && key.Namespace == r.held.Namespace {
		r.held.DeepCopyInto(c)
		return nil
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

// meddlingReader runs before once, the first time it is asked for name,
// and then answers as its Reader does.
type meddlingReader struct {
	client.Reader
	name   string
	before func()
	once   sync.Once
}

func (r *meddlingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if key.Name == r.name {
		r.once.Do(r.before)
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

// TestTheCollectorWorksFromWhatChanged is ADR-0025's part 4 for the
// collector. It read every owned row each 30 seconds and asked the
// apiserver for each distinct owner by name: 24 GETs a second on talos for
// as long as the manager ran (2026-11-28). An owner the manager's cache
// holds under the reference's uid costs the apiserver nothing; "gone" is
// still the apiserver's to say, so a cache can only keep an object, never
// delete one; an owner's delete, reported, collects its objects with no
// sweep, the cache not being asked about an owner reported gone; the cache
// is read for the listed owner kinds alone; and an object re-owned between
// the read and the delete is left.
func TestTheCollectorWorksFromWhatChanged(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if os.Getenv("CLUSTARR_REQUIRE_TOOLS") == "1" {
			t.Fatal("KUBEBUILDER_ASSETS is unset and CLUSTARR_REQUIRE_TOOLS=1")
		}
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	st := librarytest.Start(t).Store(t, nil)
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, env.Stop()) })
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1.AddToScheme(scheme))
	api, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	c := readwrite.NewClient(api, st, false, "clustarr")
	ctx := t.Context()
	comicKind := catalogv1.GroupVersion.WithKind("Comic")

	comic := func(name string) *catalogv1.Comic {
		cm := &catalogv1.Comic{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: catalogv1.ComicSpec{
				Source: catalogv1.ComicSourceComicVine, SourceID: "4050-" + name,
				QualityProfileRef: "any", RootFolderRef: "comics",
			},
		}
		require.NoError(t, c.Create(ctx, cm))
		return cm
	}
	issueOf := func(name string, owners ...*catalogv1.Comic) {
		iss := &catalogv1.Issue{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       catalogv1.IssueSpec{ComicRef: "x", Number: "1", CalculatedNumberCentis: 100},
		}
		for _, owner := range owners {
			require.NoError(t, controllerutil.SetOwnerReference(owner, iss, scheme))
		}
		require.NoError(t, c.Create(ctx, iss))
	}
	gone := func(name string) bool {
		var iss catalogv1.Issue
		err := c.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, &iss)
		if apierrors.IsNotFound(err) {
			return true
		}
		require.NoError(t, err)
		return false
	}

	saga, girls, monstress := comic("saga"), comic("paper-girls"), comic("monstress")
	issueOf("saga-1", saga)
	issueOf("saga-2", saga)
	issueOf("paper-girls-1", girls)
	issueOf("monstress-1", monstress)

	t.Log("every owner is in the cache: the apiserver is not asked")
	owners := &countingReader{Reader: api}
	cache := &countingReader{Reader: api}
	collector := &readwrite.Collector{
		Store: st, Owners: owners, Cache: cache, Scheme: scheme,
		OwnerKinds: []schema.GroupVersionKind{comicKind},
	}
	n, err := collector.Sweep(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Zero(t, owners.gets.Load(), "an owner the cache holds under the reference's uid is there")
	assert.Equal(t, int64(3), cache.gets.Load(), "each distinct owner is asked for once")

	t.Log("the cache is read for the listed owner kinds alone")
	unlisted := &readwrite.Collector{Store: st, Owners: owners, Cache: cache, Scheme: scheme}
	n, err = unlisted.Sweep(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Equal(t, int64(3), cache.gets.Load(), "a kind outside OwnerKinds is never read from the cache: the read would start an informer of it")
	assert.Equal(t, int64(3), owners.gets.Load(), "it is the apiserver's to answer")
	owners.gets.Store(0)

	t.Log("an owner the cache does not hold is the apiserver's to judge, and is collected")
	require.NoError(t, c.Delete(ctx, saga))
	n, err = collector.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, int64(1), owners.gets.Load(), "one question for the one owner the cache lacked")
	assert.True(t, gone("saga-1"))
	assert.True(t, gone("saga-2"))

	t.Log("a cache that has not seen a delete keeps the objects at a sweep")
	require.NoError(t, c.Delete(ctx, girls))
	collector.Cache = &staleReader{Reader: api, held: girls}
	n, err = collector.Sweep(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "a cache only ever says an owner is there")
	assert.False(t, gone("paper-girls-1"))

	t.Log("the delete, reported, collects them with no sweep, the stale cache not asked; Run asks for the listed kinds' events once")
	var (
		mu      sync.Mutex
		watched []schema.GroupVersionKind
	)
	collector.Watch = func(_ context.Context, gvk schema.GroupVersionKind) error {
		mu.Lock()
		defer mu.Unlock()
		watched = append(watched, gvk)
		return nil
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = collector.Run(runCtx)
	}()
	t.Cleanup(func() { stop(); <-done })
	require.Never(t, func() bool { return gone("paper-girls-1") }, 300*time.Millisecond, 50*time.Millisecond,
		"Run's first sweep meets the same stale cache")
	collector.OwnerDeleted(comicKind, girls.Namespace, girls.Name, girls.UID)
	require.Eventually(t, func() bool { return gone("paper-girls-1") }, 10*time.Second, 50*time.Millisecond)
	assert.False(t, gone("monstress-1"), "another owner's object is not touched")
	mu.Lock()
	assert.Equal(t, []schema.GroupVersionKind{comicKind}, watched)
	mu.Unlock()

	t.Log("a report about an owner that is there deletes nothing")
	collector.OwnerDeleted(comicKind, monstress.Namespace, monstress.Name, monstress.UID)
	require.Never(t, func() bool { return gone("monstress-1") }, 500*time.Millisecond, 50*time.Millisecond)

	t.Log("a delete seen without its object is a sweep")
	collector.Cache = cache
	lazarus := comic("lazarus")
	issueOf("lazarus-1", lazarus)
	require.NoError(t, c.Delete(ctx, lazarus))
	require.Never(t, func() bool { return gone("lazarus-1") }, 300*time.Millisecond, 50*time.Millisecond, "nothing reported it")
	collector.SweepSoon()
	require.Eventually(t, func() bool { return gone("lazarus-1") }, 10*time.Second, 50*time.Millisecond)
	stop()
	<-done

	t.Log("an object with one owner gone and another in the cache is kept, the apiserver asked only about the one reported")
	descender, ascender := comic("descender"), comic("ascender")
	issueOf("crossover-1", descender, ascender)
	require.NoError(t, c.Delete(ctx, descender))
	owners.gets.Store(0)
	two := &readwrite.Collector{Store: st, Owners: owners, Cache: cache, Scheme: scheme, OwnerKinds: []schema.GroupVersionKind{comicKind}}
	twoCtx, stopTwo := context.WithCancel(ctx)
	twoDone := make(chan struct{})
	go func() {
		defer close(twoDone)
		_ = two.Run(twoCtx)
	}()
	require.Eventually(t, func() bool { return owners.gets.Load() == 1 }, 10*time.Second, 20*time.Millisecond,
		"the start sweep asks the apiserver about the one owner the cache lacks")
	two.OwnerDeleted(comicKind, descender.Namespace, descender.Name, descender.UID)
	require.Eventually(t, func() bool { return owners.gets.Load() == 2 }, 10*time.Second, 20*time.Millisecond,
		"the reported owner is asked for at the apiserver")
	require.Never(t, func() bool { return gone("crossover-1") || owners.gets.Load() > 2 }, 500*time.Millisecond, 50*time.Millisecond,
		"its other owner is there by the cache, with no question")
	stopTwo()
	<-twoDone

	t.Log("an object adopted by a new owner between the read and the delete is left")
	// crossover-1's second owner goes too, so both are gone when its rows
	// are read. While the collector asks about that second owner, the row
	// is written (as a parent made again under its name adopts it in
	// place): the delete is held to the version that was read.
	require.NoError(t, c.Delete(ctx, ascender))
	adopt := func() {
		var iss catalogv1.Issue
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "default", Name: "crossover-1"}, &iss))
		iss.OwnerReferences = nil
		require.NoError(t, controllerutil.SetOwnerReference(monstress, &iss, scheme))
		require.NoError(t, c.Update(ctx, &iss))
	}
	racing := &readwrite.Collector{
		Store: st, Scheme: scheme, OwnerKinds: []schema.GroupVersionKind{comicKind},
		Owners: &meddlingReader{Reader: api, name: "ascender", before: adopt},
	}
	racing.OwnerDeleted(comicKind, descender.Namespace, descender.Name, descender.UID)
	n, err = readwrite.CollectPending(ctx, racing)
	require.NoError(t, err)
	assert.Zero(t, n, "the row changed after its owners were read")
	assert.False(t, gone("crossover-1"))
	n, err = racing.Sweep(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "and its new owner is there")
	assert.False(t, gone("crossover-1"))
}
