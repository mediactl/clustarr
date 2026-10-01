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

package k8s_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func newCASIndexer(t *testing.T, ctx context.Context, c client.Client, ns, name string) *indexv1alpha1.Indexer {
	t.Helper()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && !isAlreadyExists(err) {
		t.Fatalf("create namespace: %v", err)
	}
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: "http://fixture.invalid",
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolTorrent},
		},
	}
	if err := c.Create(ctx, idx); err != nil {
		t.Fatalf("create indexer: %v", err)
	}
	return idx
}

// increment is a render that adds n to status.indexedReleases, read off the
// object it is handed -- the read-modify-write every counter writer does.
func increment(name, ns string, n int64) func(*indexv1alpha1.Indexer) (*indexac.IndexerApplyConfiguration, bool, error) {
	return func(fresh *indexv1alpha1.Indexer) (*indexac.IndexerApplyConfiguration, bool, error) {
		return indexac.Indexer(name, ns).WithStatus(indexac.IndexerStatus().
			WithIndexedReleases(fresh.Status.IndexedReleases + n)), false, nil
	}
}

// The lost update PatchStatusCAS exists for: a second writer lands between
// the read a render was seeded from and the apply. Without the
// resourceVersion precondition the apply wins with a stale value and the
// other writer's increment is gone, and no "manager X released field Y"
// test can see it, because every field is declared.
func TestPatchStatusCASRedoesARenderAnotherWriterOvertook(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	idx := newCASIndexer(t, ctx, c, "cas-interleave", "interleave")
	key := client.ObjectKeyFromObject(idx)

	renders := 0
	fresh, applied, err := k8s.PatchStatusCAS(ctx, c, c, k8s.ManagerIndexarrWorker, key,
		func() *indexv1alpha1.Indexer { return &indexv1alpha1.Indexer{} },
		func(fresh *indexv1alpha1.Indexer) (*indexac.IndexerApplyConfiguration, bool, error) {
			renders++
			if renders == 1 {
				// The interleaved writer: a real apply under the SAME
				// manager, after this render's read.
				if _, _, err := k8s.PatchStatusCAS(ctx, c, c, k8s.ManagerIndexarrWorker, key,
					func() *indexv1alpha1.Indexer { return &indexv1alpha1.Indexer{} },
					increment(key.Name, key.Namespace, 5), 3); err != nil {
					t.Fatalf("interleaved writer: %v", err)
				}
			}
			return increment(key.Name, key.Namespace, 1)(fresh)
		}, 3)
	if err != nil {
		t.Fatalf("PatchStatusCAS: %v", err)
	}
	if !applied {
		t.Fatal("PatchStatusCAS reported nothing applied")
	}
	if renders != 2 {
		t.Errorf("renders = %d, want 2: the overtaken render must be redone from a fresh read", renders)
	}
	if fresh.Status.IndexedReleases != 5 {
		t.Errorf("the render that applied was seeded from indexedReleases=%d, want the interleaved writer's 5",
			fresh.Status.IndexedReleases)
	}

	var got indexv1alpha1.Indexer
	if err := c.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.IndexedReleases != 6 {
		t.Errorf("indexedReleases = %d, want 6: one of the two increments was lost", got.Status.IndexedReleases)
	}
}

// Many writers at once, as every replica of a worker is: no increment lost.
func TestPatchStatusCASLosesNoConcurrentIncrement(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	idx := newCASIndexer(t, ctx, c, "cas-concurrent", "concurrent")
	key := client.ObjectKeyFromObject(idx)

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		wg.Go(func() {
			_, _, err := k8s.PatchStatusCAS(ctx, c, c, k8s.ManagerIndexarrWorker, key,
				func() *indexv1alpha1.Indexer { return &indexv1alpha1.Indexer{} },
				increment(key.Name, key.Namespace, 1), 4*writers)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("PatchStatusCAS: %v", err)
		}
	}

	var got indexv1alpha1.Indexer
	if err := c.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.IndexedReleases != writers {
		t.Errorf("indexedReleases = %d, want %d", got.Status.IndexedReleases, writers)
	}
}

// A render that declines applies nothing and reports so; a render error is
// returned as is; a missing object is the reader's NotFound; and attempts
// that all conflict end in the conflict, not a silent success.
func TestPatchStatusCASSkipErrorsAndExhaustion(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	idx := newCASIndexer(t, ctx, c, "cas-edges", "edges")
	key := client.ObjectKeyFromObject(idx)
	newObj := func() *indexv1alpha1.Indexer { return &indexv1alpha1.Indexer{} }

	_, applied, err := k8s.PatchStatusCAS(ctx, c, c, k8s.ManagerIndexarrWorker, key, newObj,
		func(*indexv1alpha1.Indexer) (*indexac.IndexerApplyConfiguration, bool, error) {
			return nil, true, nil
		}, 3)
	if err != nil || applied {
		t.Errorf("skip: applied=%v err=%v, want false, nil", applied, err)
	}

	boom := errors.New("boom")
	_, _, err = k8s.PatchStatusCAS(ctx, c, c, k8s.ManagerIndexarrWorker, key, newObj,
		func(*indexv1alpha1.Indexer) (*indexac.IndexerApplyConfiguration, bool, error) {
			return nil, false, boom
		}, 3)
	if !errors.Is(err, boom) {
		t.Errorf("render error: got %v, want boom", err)
	}

	_, _, err = k8s.PatchStatusCAS(ctx, c, c, k8s.ManagerIndexarrWorker,
		client.ObjectKey{Namespace: key.Namespace, Name: "absent"}, newObj,
		increment("absent", key.Namespace, 1), 3)
	if !apierrors.IsNotFound(err) {
		t.Errorf("missing object: got %v, want NotFound", err)
	}

	// Every render is overtaken by a write of its own, so every apply
	// conflicts.
	_, _, err = k8s.PatchStatusCAS(ctx, c, c, k8s.ManagerIndexarrWorker, key, newObj,
		func(fresh *indexv1alpha1.Indexer) (*indexac.IndexerApplyConfiguration, bool, error) {
			if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerIndexarr, indexac.Indexer(key.Name, key.Namespace).
				WithStatus(indexac.IndexerStatus().WithObservedGeneration(fresh.Status.ObservedGeneration+1))); err != nil {
				t.Fatalf("overtaking write: %v", err)
			}
			return increment(key.Name, key.Namespace, 1)(fresh)
		}, 2)
	if !apierrors.IsConflict(err) {
		t.Errorf("exhausted attempts: got %v, want a Conflict", err)
	}
}
