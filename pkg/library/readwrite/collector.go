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

package readwrite

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/library"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Collector deletes the library objects whose owners are gone (design
// §3.4). A Comic's Issues, a Series' Episodes and an Artist's Albums carry
// an owner reference to their parent, and while they were custom resources
// the apiserver's garbage collector deleted them with it. A parent in
// Kubernetes cannot reach a row, so this does what that collector did, by
// its rule: an object every one of whose owners is absent -- not found, or
// found under another uid -- is deleted. The delete is the store's: an
// object with finalizers is marked and goes when they are removed, so an
// item's own cleanup runs as it did.
type Collector struct {
	Store *library.Store
	// Owners reads the owners from the apiserver itself, never a cache: an
	// owner a cache has not seen yet is not gone.
	Owners client.Reader
	// Interval is the time between sweeps; zero is DefaultCollectInterval.
	Interval time.Duration
}

// DefaultCollectInterval is how long a deleted parent's children may
// outlive it.
const DefaultCollectInterval = 30 * time.Second

// Run sweeps until ctx ends.
func (c *Collector) Run(ctx context.Context) error {
	ctx = logging.Subsystem(ctx, "library.collect")
	log := logging.FromContext(ctx)
	interval := c.Interval
	if interval <= 0 {
		interval = DefaultCollectInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if n, err := c.Sweep(ctx); err != nil && ctx.Err() == nil {
			log.Warn("collecting the library objects whose owners are gone failed; it is tried again", "err", err)
		} else if n > 0 {
			log.Info("deleted the library objects whose owners are gone", "objects", n)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// owner is one owner reference, in the namespace of the objects that name it.
type owner struct {
	namespace, apiVersion, kind, name string
	uid                               types.UID
}

// Sweep deletes every library object all of whose owners are absent and
// answers how many it deleted.
func (c *Collector) Sweep(ctx context.Context) (int, error) {
	present := map[owner]bool{}
	deleted := 0
	for _, sch := range c.Store.Schemas().Sorted() {
		n, err := c.sweepKind(ctx, sch, present)
		deleted += n
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

func (c *Collector) sweepKind(ctx context.Context, sch *library.Schema, present map[owner]bool) (int, error) {
	// Every object that names an owner and is not already going, with its
	// owners: few distinct owners stand behind many objects (a Series
	// behind its Episodes), so each owner is asked for once a sweep.
	rows, err := c.Store.DB().QueryContext(ctx, `
SELECT t.namespace, t.name, t.body->'metadata'->>'uid',
       o->>'apiVersion', o->>'kind', o->>'name', o->>'uid'
FROM library.`+sch.Table+` t, jsonb_array_elements(t.body->'metadata'->'ownerReferences') o
WHERE jsonb_typeof(t.body->'metadata'->'ownerReferences') = 'array'
  AND t.body->'metadata'->>'deletionTimestamp' IS NULL
ORDER BY t.namespace, t.name`)
	if err != nil {
		return 0, fmt.Errorf("library: read the owners of the %s objects: %w", sch.GVK.Kind, err)
	}
	type object struct {
		namespace, name string
		uid             types.UID
		owners          []owner
	}
	var objects []object
	for rows.Next() {
		var (
			ns, name, uid string
			o             owner
			ownerUID      string
		)
		if err := rows.Scan(&ns, &name, &uid, &o.apiVersion, &o.kind, &o.name, &ownerUID); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("library: read the owners of the %s objects: %w", sch.GVK.Kind, err)
		}
		o.namespace, o.uid = ns, types.UID(ownerUID)
		if n := len(objects); n > 0 && objects[n-1].namespace == ns && objects[n-1].name == name {
			objects[n-1].owners = append(objects[n-1].owners, o)
			continue
		}
		objects = append(objects, object{namespace: ns, name: name, uid: types.UID(uid), owners: []owner{o}})
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("library: read the owners of the %s objects: %w", sch.GVK.Kind, err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("library: read the owners of the %s objects: %w", sch.GVK.Kind, err)
	}

	deleted := 0
	for _, obj := range objects {
		orphan := true
		for _, o := range obj.owners {
			here, known := present[o]
			if !known {
				var err error
				if here, err = c.exists(ctx, o); err != nil {
					return deleted, err
				}
				present[o] = here
			}
			if here {
				orphan = false
				break
			}
		}
		if !orphan {
			continue
		}
		// The uid holds the delete to the object that was read: one made
		// again under the name since is another object, with its own owner.
		err := c.Store.Delete(ctx, sch, obj.namespace, obj.name, library.Preconditions{UID: obj.uid})
		switch {
		case err == nil:
			deleted++
		case apierrors.IsNotFound(err) || apierrors.IsConflict(err):
		default:
			return deleted, err
		}
	}
	return deleted, nil
}

// exists reports whether the owner is there under the uid the reference
// names: in the store when it is a library kind, in the apiserver otherwise.
func (c *Collector) exists(ctx context.Context, o owner) (bool, error) {
	gv, err := schema.ParseGroupVersion(o.apiVersion)
	if err != nil {
		// A reference nothing can resolve keeps its object: deleting on a
		// malformed reference would be a guess.
		return true, nil
	}
	gvk := gv.WithKind(o.kind)
	if sch := c.Store.Schemas().For(gvk); sch != nil {
		got, err := c.Store.Get(ctx, sch, o.namespace, o.name, false)
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return got.GetUID() == o.uid, nil
	}
	meta := &metav1.PartialObjectMetadata{}
	meta.SetGroupVersionKind(gvk)
	err = c.Owners.Get(ctx, types.NamespacedName{Namespace: o.namespace, Name: o.name}, meta)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("library: read the owner %s %s/%s: %w", o.kind, o.namespace, o.name, err)
	}
	return meta.GetUID() == o.uid, nil
}
