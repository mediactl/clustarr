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

package importlist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importliststate"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// A list's spec.listed -- tags, labels and annotations -- goes on every
// catalog item the list names, whoever added the item (owner, 2026-11-17:
// "import lists should be able to add additional tags and metadata to each
// item. This way it's easy to bundle them back into a collection"). It is
// applied here, at the end of each kind's diff, to the items the diff
// remembers, under a field manager of the list's own
// (k8s.ManagerImportListed): server-side apply then keeps two lists' marks
// on one item apart, a changed block replaces the list's own marks and
// nobody else's, and an empty apply takes them off. Each remembered item
// records the block it carries (StoredItem.Listed), so a sync writes only
// to an item whose block moved or whose marks someone removed.

// reservedListedKeys are the label and annotation key parts a list may not
// set: clustarr's own annotations are instructions (catalog.clustarr.io/
// delete removes an item's files), and Kubernetes' are its own. The CRD
// refuses them at admission; this is the same verdict for a list admitted
// before it carried the rule.
var reservedListedKeys = []string{"clustarr.io/", "kubernetes.io/", "k8s.io/"}

// listedMarks is a list's spec.listed, cleaned: what an item is to carry.
type listedMarks struct {
	tags        []string
	labels      map[string]string
	annotations map[string]string
}

// marksOf cleans and checks a list's spec.listed. An error names the entry
// the apiserver would refuse on every item, or a reserved key.
func marksOf(l *catalogv1alpha1.ListedMetadata) (listedMarks, error) {
	var m listedMarks
	if l == nil {
		return m, nil
	}
	seen := map[string]bool{}
	for _, t := range l.Tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		m.tags = append(m.tags, t)
	}
	sort.Strings(m.tags)
	for k, v := range l.Labels {
		if err := listedKey(k); err != nil {
			return listedMarks{}, fmt.Errorf("spec.listed.labels: %w", err)
		}
		if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
			return listedMarks{}, fmt.Errorf("spec.listed.labels: %q is no label value for %q: %s", v, k, strings.Join(errs, "; "))
		}
	}
	for k := range l.Annotations {
		if err := listedKey(k); err != nil {
			return listedMarks{}, fmt.Errorf("spec.listed.annotations: %w", err)
		}
	}
	if len(l.Labels) > 0 {
		m.labels = l.Labels
	}
	if len(l.Annotations) > 0 {
		m.annotations = l.Annotations
	}
	return m, nil
}

// listedKey refuses a label or annotation key Kubernetes would, and a
// reserved one.
func listedKey(k string) error {
	if errs := validation.IsQualifiedName(k); len(errs) > 0 {
		return fmt.Errorf("%q is no key: %s", k, strings.Join(errs, "; "))
	}
	for _, r := range reservedListedKeys {
		if strings.Contains(k, r) {
			return fmt.Errorf("%q is a %s key, which a list may not set", k, strings.TrimSuffix(r, "/"))
		}
	}
	return nil
}

// empty reports whether the list puts nothing on its items.
func (m listedMarks) empty() bool {
	return len(m.tags) == 0 && len(m.labels) == 0 && len(m.annotations) == 0
}

// hash names the marks: what a remembered item records as carried. "" for
// none.
func (m listedMarks) hash() string {
	if m.empty() {
		return ""
	}
	b, _ := json.Marshal(struct {
		Tags        []string          `json:"tags"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	}{m.tags, m.labels, m.annotations})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// on reports whether it already carries every mark, so a sync leaves an
// item nobody touched alone.
func (m listedMarks) on(it libraryItem) bool {
	for _, t := range m.tags {
		if !slices.Contains(it.tags, t) {
			return false
		}
	}
	for k, v := range m.labels {
		if got, ok := it.labels[k]; !ok || got != v {
			return false
		}
	}
	for k, v := range m.annotations {
		if got, ok := it.annotations[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// errListedGone is an item that went between the diff's read of the library
// and the write: nothing is left to mark or to clear.
var errListedGone = errors.New("importlist: the listed item is gone")

// applyListed makes the list's marks on the item exactly m, as one
// declaration under the list's own field manager; empty marks take the
// list's off. The item's UID is pinned, so an item deleted meanwhile is
// never created from the marks alone.
func (r *Reconciler) applyListed(
	ctx context.Context, il *catalogv1alpha1.ImportList, kind commonv1.MediaKind, it libraryItem, m listedMarks,
) error {
	fm := k8s.ManagerImportListed(il.Name)
	var err error
	switch kind {
	case commonv1.MediaKindMovie:
		ac := catalogac.Movie(it.name, il.Namespace).WithUID(it.uid)
		if len(m.labels) > 0 {
			ac = ac.WithLabels(m.labels)
		}
		if len(m.annotations) > 0 {
			ac = ac.WithAnnotations(m.annotations)
		}
		if len(m.tags) > 0 {
			ac = ac.WithSpec(catalogac.MovieSpec().WithTags(m.tags...))
		}
		_, err = k8s.Apply(ctx, r.Client, fm, ac)
	case commonv1.MediaKindSeries:
		ac := catalogac.Series(it.name, il.Namespace).WithUID(it.uid)
		if len(m.labels) > 0 {
			ac = ac.WithLabels(m.labels)
		}
		if len(m.annotations) > 0 {
			ac = ac.WithAnnotations(m.annotations)
		}
		if len(m.tags) > 0 {
			ac = ac.WithSpec(catalogac.SeriesSpec().WithTags(m.tags...))
		}
		_, err = k8s.Apply(ctx, r.Client, fm, ac)
	default:
		return fmt.Errorf("importlist: a %s takes no listed marks", kind)
	}
	switch {
	case err == nil:
		return nil
	case apierrors.IsNotFound(err), apierrors.IsConflict(err), apierrors.IsInvalid(err):
		// A pinned UID that names no object: the item is gone (the apiserver
		// answers a create with a UID as a conflict, and one without the
		// kind's required fields as invalid).
		return fmt.Errorf("%w: %s", errListedGone, it.name)
	default:
		return err
	}
}

// listedResult is what markListed did.
type listedResult struct {
	marked, cleared int
	errs            []error
}

// listedWriters is how many items' marks are written at once. A write is
// an apiserver round trip, a quarter of a second on talos, and a list
// names hundreds of items inside one reconcile's deadline: one at a time,
// 755 films took longer than the five minutes a reconcile has
// (2026-11-17), the pass lost what it had done and the next began again.
const listedWriters = 8

// listedWrite is one item whose marks are to be made m.
type listedWrite struct {
	it libraryItem
	m  listedMarks
}

// applyListedAll makes each write, listedWriters at a time, and answers
// each one's error in order. Once ctx is done nothing more is sent.
func (r *Reconciler) applyListedAll(
	ctx context.Context, il *catalogv1alpha1.ImportList, kind commonv1.MediaKind, writes []listedWrite,
) []error {
	errs := make([]error, len(writes))
	var wg sync.WaitGroup
	next := make(chan int)
	for range min(listedWriters, len(writes)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				if err := ctx.Err(); err != nil {
					errs[i] = err
					continue
				}
				errs[i] = r.applyListed(ctx, il, kind, writes[i].it, writes[i].m)
			}
		}()
	}
	for i := range writes {
		next <- i
	}
	close(next)
	wg.Wait()
	return errs
}

// markListed brings the list's marks to the items this diff remembers and
// takes them off the ones it no longer does. remembered is updated in
// place with what each item now carries; existing is what the last diff
// remembered. An item not in the library (an entry the list only names, an
// id that did not resolve) carries nothing.
//
// An item that carries every mark while nothing is recorded for it is
// taken as marked, without a write: the record is lost when a pass ends
// past its deadline, and the next must go on from where that one stopped,
// not begin again.
func (r *Reconciler) markListed(
	ctx context.Context, il *catalogv1alpha1.ImportList, kind commonv1.MediaKind, m listedMarks,
	existing, remembered []importliststate.StoredItem, lib library,
) listedResult {
	var res listedResult
	hash := m.hash()
	carried := map[string]string{}
	for _, si := range existing {
		if si.Listed != "" {
			carried[si.ObjectName] = si.Listed
		}
	}

	// Decide first, write together, then record what each item carries.
	var writes []listedWrite
	writeOf := map[string]int{} // object name -> its index in writes
	named := map[string]bool{}
	for i := range remembered {
		e := &remembered[i]
		if named[e.ObjectName] {
			continue
		}
		named[e.ObjectName] = true
		e.Listed = carried[e.ObjectName]
		it, held := lib.byName[e.ObjectName]
		if !held && !e.ListedOnly {
			// An item this diff has just created is not in the library it
			// read first.
			it, held = r.addedItem(ctx, il.Namespace, *e)
		}
		switch {
		case !held:
			e.Listed = ""
		case hash == "" && e.Listed == "":
		case hash != "" && (e.Listed == hash || e.Listed == "") && m.on(it):
			e.Listed = hash
		default:
			writeOf[e.ObjectName] = len(writes)
			writes = append(writes, listedWrite{it: it, m: m})
		}
	}
	marks := len(writes)
	var dropped []string
	for name := range carried {
		if named[name] {
			continue
		}
		if it, held := lib.byName[name]; held {
			dropped = append(dropped, name)
			writes = append(writes, listedWrite{it: it})
		}
	}

	errs := r.applyListedAll(ctx, il, kind, writes)
	now := map[string]string{}
	for i := range remembered {
		e := &remembered[i]
		if h, ok := now[e.ObjectName]; ok {
			e.Listed = h
			continue
		}
		if w, ok := writeOf[e.ObjectName]; ok {
			switch err := errs[w]; {
			case err == nil && hash == "":
				res.cleared++
				e.Listed = ""
			case err == nil:
				res.marked++
				e.Listed = hash
			case errors.Is(err, errListedGone):
				e.Listed = ""
			default:
				// e.Listed stays what the item carried, so the next sync
				// tries again.
				res.errs = append(res.errs, fmt.Errorf("mark %s: %w", e.ObjectName, err))
			}
		}
		now[e.ObjectName] = e.Listed
	}
	for i, name := range dropped {
		switch err := errs[marks+i]; {
		case err == nil:
			res.cleared++
		case errors.Is(err, errListedGone):
		default:
			res.errs = append(res.errs, fmt.Errorf("clear %s: %w", name, err))
		}
	}
	return res
}

// addedItem reads an item the list added, past the cache, which may not
// hold one created a moment ago.
func (r *Reconciler) addedItem(ctx context.Context, namespace string, si importliststate.StoredItem) (libraryItem, bool) {
	key := types.NamespacedName{Namespace: namespace, Name: si.ObjectName}
	var obj client.Object
	var tags func() []string
	if si.ObjectKind == string(commonv1.MediaKindSeries) {
		s := &catalogv1alpha1.Series{}
		obj, tags = s, func() []string { return s.Spec.Tags }
	} else {
		m := &catalogv1alpha1.Movie{}
		obj, tags = m, func() []string { return m.Spec.Tags }
	}
	if err := r.apiReader().Get(ctx, key, obj); err != nil {
		return libraryItem{}, false
	}
	return libraryItem{
		name: obj.GetName(), uid: obj.GetUID(),
		tags: tags(), labels: obj.GetLabels(), annotations: obj.GetAnnotations(),
	}, true
}

// anyListed reports whether a remembered item carries the list's marks.
func anyListed(items []importliststate.StoredItem) bool {
	for _, si := range items {
		if si.Listed != "" {
			return true
		}
	}
	return false
}

// listedKinds are the kinds a list's marks go on: the ones an import list
// has a catalog writer for.
var listedKinds = []commonv1.MediaKind{commonv1.MediaKindMovie, commonv1.MediaKindSeries}

// clearListed takes the list's marks off every item it remembers, for a
// list being deleted, and forgets them. Every remembered item in the
// library is cleared, recorded as marked or not: the record is lost when a
// pass ends past its deadline, and an empty apply by a manager that owns
// nothing changes nothing. It fails when an item could not be cleared, so
// the finalizer stays until all are.
func (r *Reconciler) clearListed(ctx context.Context, il *catalogv1alpha1.ImportList) error {
	log := logging.FromContext(ctx)
	kv := r.snapshots()
	for _, kind := range listedKinds {
		items, rev, err := importliststate.LoadItems(ctx, kv, il.Namespace, il.Name, string(kind))
		if err != nil {
			return fmt.Errorf("load remembered items: %w", err)
		}
		if len(items) == 0 {
			continue
		}
		lib, err := loadLibrary(ctx, r.Client, il.Namespace, il.Name, kind)
		if err != nil {
			return err
		}
		var writes []listedWrite
		seen := map[string]bool{}
		for i := range items {
			it, held := lib.byName[items[i].ObjectName]
			if held && !seen[it.name] {
				seen[it.name] = true
				writes = append(writes, listedWrite{it: it})
			}
			items[i].Listed = ""
		}
		for i, err := range r.applyListedAll(ctx, il, kind, writes) {
			if err != nil && !errors.Is(err, errListedGone) {
				return fmt.Errorf("clear %s: %w", writes[i].it.name, err)
			}
		}
		if err := importliststate.SaveItems(ctx, kv, il.Namespace, il.Name, string(kind), rev, items); err != nil &&
			!errors.Is(err, importliststate.ErrItemsChangedConcurrently) {
			log.Warn("importlist: could not save remembered items after clearing the list's marks", "kind", kind, "err", err)
		}
		log.Info("importlist: the deleted list's marks were taken off its items", "kind", kind, "items", len(writes))
	}
	return nil
}

// ensureListedFinalizer holds a list that marks its items until its marks
// are off them: the finalizer is declared, alone, under the controller's
// own manager on the list, so declaring nothing releases it.
func (r *Reconciler) ensureListedFinalizer(ctx context.Context, il *catalogv1alpha1.ImportList, want bool) error {
	has := k8s.HasFinalizer(il, catalogv1alpha1.ImportListFinalizerListed)
	if has == want {
		return nil
	}
	ac := catalogac.ImportList(il.Name, il.Namespace).WithUID(il.UID)
	if want {
		ac = ac.WithFinalizers(catalogv1alpha1.ImportListFinalizerListed)
	}
	if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerImport, ac); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
			return nil
		}
		return err
	}
	return nil
}
