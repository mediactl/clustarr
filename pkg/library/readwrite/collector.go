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
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/library"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
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
//
// It works from what changed (ADR-0025, part 4). It used to read every
// owned row of every kind each 30 seconds and ask the apiserver for each
// distinct owner by name: about 1,100 owners behind 35,000 rows on talos,
// 24 GETs a second for as long as the manager ran, against control planes
// whose apiservers had been OOM-killed that month (2026-11-28). Now:
//
//   - an owner's delete is an event ([Collector.OwnerDeleted], fed by the
//     manager's informers of OwnerKinds), and only that owner's objects
//     are read;
//   - one sweep when Run starts covers what was deleted while no manager
//     ran, reading the distinct owners and not the rows; there is no timer;
//   - an owner of one of OwnerKinds that the manager's cache holds under
//     the uid the reference names is there (Cache). Absence is still the
//     apiserver's to say: an owner a cache has not seen yet is not gone, so
//     every "not there" is confirmed through Owners before anything is
//     deleted, and an owner reported deleted is asked for there directly.
//
// So the cache can only keep an object; it never deletes one.
type Collector struct {
	Store *library.Store
	// Owners reads the owners from the apiserver itself, never a cache. It
	// is asked whenever Cache cannot vouch for an owner.
	Owners client.Reader
	// OwnerKinds are the kinds whose controllers set owner references on
	// library objects, and that the manager's cache already holds: the only
	// kinds Cache is read for and Watch is called for. Reading a cache for
	// any other kind would start an informer of it -- cluster-wide, or for
	// a library kind a row informer -- and wait for it to sync.
	OwnerKinds []schema.GroupVersionKind
	// Cache, when set with Scheme, is asked first about an owner of one of
	// OwnerKinds. It only ever answers "there".
	Cache client.Reader
	// Scheme builds the typed object Cache is read into, so the read is
	// served by the informer the manager already runs for the kind and
	// starts no metadata informer beside it.
	Scheme *runtime.Scheme
	// Watch, when set, is called for each of OwnerKinds when Run starts:
	// the caller feeds that kind's deletes to OwnerDeleted from then on. A
	// kind it fails for is logged; its deletes are then collected at the
	// next start's sweep.
	Watch func(ctx context.Context, gvk schema.GroupVersionKind) error
	// RetryBackoff is the wait before a failed sweep or collection is tried
	// again, doubling to collectRetryMax while it keeps failing; zero is
	// collectRetryBase.
	RetryBackoff time.Duration

	mu        sync.Mutex
	pending   map[owner]bool
	sweepOwed bool
	signal    chan struct{}
	// children is, for each owner kind a sweep found, the kinds whose
	// objects name it: where a reported owner's objects are looked for.
	children map[schema.GroupVersionKind]map[string]*library.Schema
	// unlisted is the owner kinds outside OwnerKinds a sweep found, each
	// logged once.
	unlisted map[schema.GroupVersionKind]bool
}

// Collection triggers and owner checks, the label values of
// clustarr_library_collect_runs_total and _checks_total.
const (
	collectSweep = "sweep"
	collectEvent = "event"

	checkCache     = "cache"
	checkAPIServer = "apiserver"
	checkStore     = "store"
)

// A failed sweep or collection is tried again after collectRetryBase,
// doubling to collectRetryMax. Nothing else would: there is no timer, and
// the next report may be weeks away once restarts are rare, so a start
// sweep that met an apiserver or database error in the start it ran in
// would leave what was deleted while no manager ran until the next start.
const (
	collectRetryBase = 30 * time.Second
	collectRetryMax  = 10 * time.Minute
)

// Run sweeps once, then collects for each deleted owner as it is reported,
// until ctx ends. What fails is owed until it succeeds.
func (c *Collector) Run(ctx context.Context) error {
	ctx = logging.Subsystem(ctx, "library.collect")
	log := logging.FromContext(ctx)
	signal := c.signalChan()
	if c.Watch != nil {
		for _, gvk := range c.OwnerKinds {
			if err := c.Watch(ctx, gvk); err != nil {
				log.Error("the deletes of a kind that owns library objects are not reported; they are collected at the next start",
					"kind", gvk.Kind, "err", err)
			}
		}
	}
	base := c.RetryBackoff
	if base <= 0 {
		base = collectRetryBase
	}
	backoff := base
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	sweep := true
	for {
		var (
			n   int
			err error
		)
		if sweep {
			n, err = c.Sweep(ctx)
		} else {
			n, err = c.collectPending(ctx)
		}
		if ctx.Err() != nil {
			return nil
		}
		// retry fires when what just failed is to be tried again; nil, it
		// never does, and Run waits for a report.
		var retry <-chan time.Time
		switch {
		case err != nil:
			if sweep {
				// A sweep that failed is owed until one succeeds. A failed
				// collection kept its owners pending itself.
				c.mu.Lock()
				c.sweepOwed = true
				c.mu.Unlock()
			}
			log.Warn("collecting the library objects whose owners are gone failed; it is tried again", "err", err, "in", backoff.String())
			timer.Reset(backoff)
			retry = timer.C
			backoff = min(2*backoff, max(base, collectRetryMax))
		default:
			backoff = base
			if n > 0 {
				log.Info("deleted the library objects whose owners are gone", "objects", n)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-signal:
			timer.Stop() // the wake does what the retry would have
		case <-retry:
		}
		c.mu.Lock()
		sweep, c.sweepOwed = c.sweepOwed, false
		c.mu.Unlock()
	}
}

// OwnerDeleted reports that the owner of kind gvk named namespace/name with
// uid was deleted: its objects are collected by Run without a sweep. It
// never blocks and may be called from an informer's handler.
func (c *Collector) OwnerDeleted(gvk schema.GroupVersionKind, namespace, name string, uid types.UID) {
	o := owner{namespace: namespace, apiVersion: gvk.GroupVersion().String(), kind: gvk.Kind, name: name, uid: uid}
	signal := c.signalChan()
	c.mu.Lock()
	if c.pending == nil {
		c.pending = map[owner]bool{}
	}
	c.pending[o] = true
	c.mu.Unlock()
	select {
	case signal <- struct{}{}:
	default: // a collection is already owed
	}
}

// SweepSoon asks Run for a whole sweep: for a delete that was seen without
// the object that went (a tombstone that carries none), so there is no
// owner to collect for. It never blocks.
func (c *Collector) SweepSoon() {
	signal := c.signalChan()
	c.mu.Lock()
	c.sweepOwed = true
	c.mu.Unlock()
	select {
	case signal <- struct{}{}:
	default:
	}
}

func (c *Collector) signalChan() chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.signal == nil {
		c.signal = make(chan struct{}, 1)
	}
	return c.signal
}

// collectPending collects for the owners OwnerDeleted reported. One that
// fails, and those after it, are kept for the next call.
func (c *Collector) collectPending(ctx context.Context) (int, error) {
	c.mu.Lock()
	owners := make([]owner, 0, len(c.pending))
	for o := range c.pending {
		owners = append(owners, o)
	}
	c.pending = nil
	c.mu.Unlock()
	if len(owners) == 0 {
		return 0, nil
	}
	metrics.LibraryCollectRunsTotal.WithLabelValues(collectEvent).Inc()

	present := map[owner]bool{}
	deleted := 0
	for i, o := range owners {
		n, err := c.collectFor(ctx, o, present)
		deleted += n
		if err != nil {
			c.mu.Lock()
			if c.pending == nil {
				c.pending = map[owner]bool{}
			}
			for _, left := range owners[i:] {
				c.pending[left] = true
			}
			c.mu.Unlock()
			return deleted, err
		}
	}
	return deleted, nil
}

// owner is one owner reference, in the namespace of the objects that name it.
type owner struct {
	namespace, apiVersion, kind, name string
	uid                               types.UID
}

// gvk is the owner's kind, false for a reference nothing can resolve.
func (o owner) gvk() (schema.GroupVersionKind, bool) {
	gv, err := schema.ParseGroupVersion(o.apiVersion)
	if err != nil {
		return schema.GroupVersionKind{}, false
	}
	return gv.WithKind(o.kind), true
}

// Sweep deletes every library object all of whose owners are absent and
// answers how many it deleted. It reads each kind's distinct owners and,
// only for an owner that is gone, the objects that name it.
func (c *Collector) Sweep(ctx context.Context) (int, error) {
	metrics.LibraryCollectRunsTotal.WithLabelValues(collectSweep).Inc()
	present := map[owner]bool{}
	children := map[schema.GroupVersionKind]map[string]*library.Schema{}
	deleted := 0
	for _, sch := range c.Store.Schemas().Sorted() {
		owners, err := c.ownersOf(ctx, sch)
		if err != nil {
			return deleted, err
		}
		for _, o := range owners {
			if gvk, ok := o.gvk(); ok {
				if children[gvk] == nil {
					children[gvk] = map[string]*library.Schema{}
				}
				children[gvk][sch.Table] = sch
				c.noteUnlisted(ctx, gvk)
			}
			here, err := c.there(ctx, o, present)
			if err != nil {
				return deleted, err
			}
			if here {
				continue
			}
			n, err := c.collectKind(ctx, sch, o, present)
			deleted += n
			if err != nil {
				return deleted, err
			}
		}
	}
	c.mu.Lock()
	c.children = children
	c.mu.Unlock()
	return deleted, nil
}

// noteUnlisted logs, once, an owner kind that is neither a library kind
// nor one of OwnerKinds: the apiserver is asked about each such owner, and
// its deletes reach the collector only at a start's sweep.
func (c *Collector) noteUnlisted(ctx context.Context, gvk schema.GroupVersionKind) {
	if c.Store.Schemas().For(gvk) != nil || slices.Contains(c.OwnerKinds, gvk) {
		return
	}
	c.mu.Lock()
	seen := c.unlisted[gvk]
	if !seen {
		if c.unlisted == nil {
			c.unlisted = map[schema.GroupVersionKind]bool{}
		}
		c.unlisted[gvk] = true
	}
	c.mu.Unlock()
	if !seen {
		logging.FromContext(ctx).Warn("a kind owns library objects and is not one the collector watches; its deletes are collected only when the manager starts",
			"kind", gvk.Kind, "group", gvk.Group)
	}
}

// ownersOf lists the distinct owners the kind's objects name, objects
// already going left out.
func (c *Collector) ownersOf(ctx context.Context, sch *library.Schema) ([]owner, error) {
	rows, err := c.Store.DB().QueryContext(ctx, `
SELECT DISTINCT t.namespace, o->>'apiVersion', o->>'kind', o->>'name', o->>'uid'
FROM library.`+sch.Table+` t, jsonb_array_elements(t.body->'metadata'->'ownerReferences') o
WHERE jsonb_typeof(t.body->'metadata'->'ownerReferences') = 'array'
  AND t.body->'metadata'->>'deletionTimestamp' IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("library: read the owners of the %s objects: %w", sch.GVK.Kind, err)
	}
	defer func() { _ = rows.Close() }()
	var owners []owner
	for rows.Next() {
		var (
			o   owner
			uid string
		)
		if err := rows.Scan(&o.namespace, &o.apiVersion, &o.kind, &o.name, &uid); err != nil {
			return nil, fmt.Errorf("library: read the owners of the %s objects: %w", sch.GVK.Kind, err)
		}
		o.uid = types.UID(uid)
		owners = append(owners, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("library: read the owners of the %s objects: %w", sch.GVK.Kind, err)
	}
	return owners, nil
}

// collectFor deletes the objects that name gone and have no owner left, in
// the kinds the last sweep found naming an owner of gone's kind (every kind
// when it found none: a kind that gained its first object since). It asks
// again whether gone is gone: a report is a reason to look, and the
// apiserver is the judge -- the cache is not asked about an owner reported
// deleted, since the report is a reason to doubt it.
func (c *Collector) collectFor(ctx context.Context, gone owner, present map[owner]bool) (int, error) {
	here, err := c.exists(ctx, gone, true)
	if err != nil {
		return 0, err
	}
	present[gone] = here
	if here {
		return 0, nil
	}
	schemas := c.Store.Schemas().Sorted()
	if gvk, ok := gone.gvk(); ok {
		c.mu.Lock()
		known := c.children[gvk]
		c.mu.Unlock()
		if len(known) > 0 {
			schemas = schemas[:0:0]
			for _, sch := range c.Store.Schemas().Sorted() {
				if known[sch.Table] != nil {
					schemas = append(schemas, sch)
				}
			}
		}
	}
	deleted := 0
	for _, sch := range schemas {
		n, err := c.collectKind(ctx, sch, gone, present)
		deleted += n
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

// collectKind deletes the kind's objects that name gone, an absent owner,
// and whose other owners are absent too.
func (c *Collector) collectKind(ctx context.Context, sch *library.Schema, gone owner, present map[owner]bool) (int, error) {
	names, err := json.Marshal([]map[string]string{{"uid": string(gone.uid)}})
	if err != nil {
		return 0, err
	}
	// Each object that names the owner and is not already going, with all
	// of its owners: the rule is every owner absent, not this one.
	rows, err := c.Store.DB().QueryContext(ctx, `
SELECT t.namespace, t.name, t.body->'metadata'->>'uid', t.version,
       o->>'apiVersion', o->>'kind', o->>'name', o->>'uid'
FROM library.`+sch.Table+` t, jsonb_array_elements(t.body->'metadata'->'ownerReferences') o
WHERE jsonb_typeof(t.body->'metadata'->'ownerReferences') = 'array'
  AND t.body->'metadata'->'ownerReferences' @> $1::jsonb
  AND t.namespace = $2
  AND t.body->'metadata'->>'deletionTimestamp' IS NULL
ORDER BY t.namespace, t.name`, string(names), gone.namespace)
	if err != nil {
		return 0, fmt.Errorf("library: read the owners of the %s objects: %w", sch.GVK.Kind, err)
	}
	type object struct {
		namespace, name string
		uid             types.UID
		version         int64
		owners          []owner
	}
	var objects []object
	for rows.Next() {
		var (
			ns, name, uid string
			version       int64
			o             owner
			ownerUID      string
		)
		if err := rows.Scan(&ns, &name, &uid, &version, &o.apiVersion, &o.kind, &o.name, &ownerUID); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("library: read the owners of the %s objects: %w", sch.GVK.Kind, err)
		}
		o.namespace, o.uid = ns, types.UID(ownerUID)
		if n := len(objects); n > 0 && objects[n-1].namespace == ns && objects[n-1].name == name {
			objects[n-1].owners = append(objects[n-1].owners, o)
			continue
		}
		objects = append(objects, object{namespace: ns, name: name, uid: types.UID(uid), version: version, owners: []owner{o}})
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
			here, err := c.there(ctx, o, present)
			if err != nil {
				return deleted, err
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
		// The version holds it to the owners that were read: a parent
		// deleted and made again under its name fans out to the same
		// child names, and adopts a row in place -- same uid, new owner --
		// in the second its delete is reported.
		err := c.Store.Delete(ctx, sch, obj.namespace, obj.name,
			library.Preconditions{UID: obj.uid, ResourceVersion: strconv.FormatInt(obj.version, 10)})
		switch {
		case err == nil:
			deleted++
			metrics.LibraryCollectDeletedTotal.WithLabelValues(sch.GVK.Kind).Inc()
		case apierrors.IsNotFound(err) || apierrors.IsConflict(err):
		default:
			return deleted, err
		}
	}
	return deleted, nil
}

// there is exists, asked once an owner a call.
func (c *Collector) there(ctx context.Context, o owner, present map[owner]bool) (bool, error) {
	if here, known := present[o]; known {
		return here, nil
	}
	here, err := c.exists(ctx, o, false)
	if err != nil {
		return false, err
	}
	present[o] = here
	return here, nil
}

// exists reports whether the owner is there under the uid the reference
// names: in the store when it is a library kind, in the apiserver otherwise.
// live leaves the cache out and asks the apiserver whatever it holds.
func (c *Collector) exists(ctx context.Context, o owner, live bool) (bool, error) {
	gvk, ok := o.gvk()
	if !ok {
		// A reference nothing can resolve keeps its object: deleting on a
		// malformed reference would be a guess.
		return true, nil
	}
	if sch := c.Store.Schemas().For(gvk); sch != nil {
		metrics.LibraryCollectChecksTotal.WithLabelValues(checkStore).Inc()
		got, err := c.Store.Get(ctx, sch, o.namespace, o.name, false)
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return got.GetUID() == o.uid, nil
	}
	if !live && c.cached(ctx, gvk, o) {
		metrics.LibraryCollectChecksTotal.WithLabelValues(checkCache).Inc()
		return true, nil
	}
	metrics.LibraryCollectChecksTotal.WithLabelValues(checkAPIServer).Inc()
	meta := &metav1.PartialObjectMetadata{}
	meta.SetGroupVersionKind(gvk)
	err := c.Owners.Get(ctx, types.NamespacedName{Namespace: o.namespace, Name: o.name}, meta)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("library: read the owner %s %s/%s: %w", o.kind, o.namespace, o.name, err)
	}
	return meta.GetUID() == o.uid, nil
}

// cached reports whether the manager's cache holds the owner under the uid
// the reference names. Anything else -- no cache, a kind outside
// OwnerKinds, a miss, another uid, an error -- is "cannot say", and the
// apiserver is asked.
func (c *Collector) cached(ctx context.Context, gvk schema.GroupVersionKind, o owner) bool {
	if c.Cache == nil || c.Scheme == nil || !slices.Contains(c.OwnerKinds, gvk) {
		return false
	}
	typed, err := c.Scheme.New(gvk)
	if err != nil {
		return false
	}
	obj, ok := typed.(client.Object)
	if !ok {
		return false
	}
	if err := c.Cache.Get(ctx, types.NamespacedName{Namespace: o.namespace, Name: o.name}, obj); err != nil {
		return false
	}
	return obj.GetUID() == o.uid
}
