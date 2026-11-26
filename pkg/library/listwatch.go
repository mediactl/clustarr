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

package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// ListerWatcher feeds an informer of one library kind from the store: a list
// is one snapshot of the kind's table with the kind's sequence at that
// snapshot, and a watch is the change feed from a sequence number on. It
// also serves a streamed initial list (a watch asked to send its initial
// events), which is how the manager's row informer starts without holding
// the kind decoded whole.
//
// Objects are the scheme's own type for the kind, decoded from the stored
// JSON, with no ownership records: no cache holds those.
type ListerWatcher struct {
	store     *Store
	sch       *Schema
	namespace string
	newObject func() client.Object
	newList   func() runtime.Object
	// typed objects carry no kind, as the apiserver's typed decode leaves
	// them; an unstructured or metadata-only object is named by its kind.
	typed bool
	// omitManaged leaves the ownership records out of every object, for a
	// cache that would strip them (every clustarr cache does): they are
	// then never read from the database at all.
	omitManaged bool
}

// WithoutManagedFields is the list-watcher answering objects without their
// ownership records.
func (lw *ListerWatcher) WithoutManagedFields() *ListerWatcher {
	c := *lw
	c.omitManaged = true
	return &c
}

var _ toolscache.ListerWatcherWithContext = (*ListerWatcher)(nil)

// ListerWatcher answers the list-watcher of a kind, of one namespace when
// it is named, in the scheme's own type for the kind.
func (s *Store) ListerWatcher(sch *Schema, scheme *runtime.Scheme, namespace string) (*ListerWatcher, error) {
	if !scheme.Recognizes(sch.GVK) || !scheme.Recognizes(sch.ListGVK()) {
		return nil, fmt.Errorf("library: the scheme does not know %s and its list", sch.GVK)
	}
	if o, err := scheme.New(sch.GVK); err != nil {
		return nil, fmt.Errorf("library: %w", err)
	} else if _, ok := o.(client.Object); !ok {
		return nil, fmt.Errorf("library: the scheme's %s is a %T, not an object", sch.GVK.Kind, o)
	}
	return &ListerWatcher{
		store: s, sch: sch, namespace: namespace, typed: true,
		newObject: func() client.Object {
			o, _ := scheme.New(sch.GVK)
			return o.(client.Object)
		},
		newList: func() runtime.Object {
			l, _ := scheme.New(sch.ListGVK())
			return l
		},
	}, nil
}

// UnstructuredListerWatcher is the list-watcher of a kind as unstructured
// objects.
func (s *Store) UnstructuredListerWatcher(sch *Schema, namespace string) *ListerWatcher {
	return &ListerWatcher{
		store: s, sch: sch, namespace: namespace,
		newObject: func() client.Object { return &unstructured.Unstructured{} },
		newList: func() runtime.Object {
			l := &unstructured.UnstructuredList{}
			l.SetGroupVersionKind(sch.ListGVK())
			return l
		},
	}
}

// MetadataListerWatcher is the list-watcher of a kind as metadata alone.
func (s *Store) MetadataListerWatcher(sch *Schema, namespace string) *ListerWatcher {
	return &ListerWatcher{
		store: s, sch: sch, namespace: namespace,
		newObject: func() client.Object { return &metav1.PartialObjectMetadata{} },
		newList: func() runtime.Object {
			l := &metav1.PartialObjectMetadataList{}
			l.SetGroupVersionKind(sch.ListGVK())
			return l
		},
	}
}

// decodeInto fills obj from an object's stored JSON and its version.
func decodeInto(raw Raw, obj client.Object) error {
	if err := json.Unmarshal(raw.Body, obj); err != nil {
		return fmt.Errorf("library: decode %s/%s: %w", raw.Namespace, raw.Name, err)
	}
	obj.SetResourceVersion(strconv.FormatInt(raw.Version, 10))
	return nil
}

func (lw *ListerWatcher) object(raw Raw) (client.Object, error) {
	obj := lw.newObject()
	if err := decodeInto(raw, obj); err != nil {
		return nil, err
	}
	if err := fillManaged(raw, obj); err != nil {
		return nil, err
	}
	if lw.typed {
		obj.GetObjectKind().SetGroupVersionKind(schema.GroupVersionKind{})
	}
	return obj, nil
}

// selection is the selectors of a list or a watch.
type selection struct {
	labels labels.Selector
	fields fields.Selector
}

func (lw *ListerWatcher) selection(opts metav1.ListOptions) (selection, error) {
	sel := selection{labels: labels.Everything(), fields: fields.Everything()}
	var err error
	if opts.LabelSelector != "" {
		if sel.labels, err = labels.Parse(opts.LabelSelector); err != nil {
			return sel, apierrors.NewBadRequest(err.Error())
		}
	}
	if opts.FieldSelector != "" {
		if sel.fields, err = fields.ParseSelector(opts.FieldSelector); err != nil {
			return sel, apierrors.NewBadRequest(err.Error())
		}
		if err := lw.sch.checkFieldSelector(sel.fields); err != nil {
			return sel, err
		}
	}
	return sel, nil
}

func (sel selection) matches(sch *Schema, raw Raw, obj client.Object) (bool, error) {
	if sel.labels != nil && !sel.labels.Matches(labels.Set(obj.GetLabels())) {
		return false, nil
	}
	if sel.fields == nil || sel.fields.Empty() {
		return true, nil
	}
	set, err := sch.fieldSet(raw)
	if err != nil {
		return false, err
	}
	return sel.fields.Matches(set), nil
}

// ListWithContext implements cache.ListerWithContext.
func (lw *ListerWatcher) ListWithContext(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
	sel, err := lw.selection(opts)
	if err != nil {
		return nil, err
	}
	items, version, err := lw.store.listObjects(ctx, lw.sch, lw.namespace, sel, !lw.omitManaged, lw.object)
	if err != nil {
		return nil, err
	}
	list := lw.newList()
	if err := meta.SetList(list, items); err != nil {
		return nil, fmt.Errorf("library: %w", err)
	}
	acc, err := meta.ListAccessor(list)
	if err != nil {
		return nil, fmt.Errorf("library: %w", err)
	}
	acc.SetResourceVersion(strconv.FormatInt(version, 10))
	return list, nil
}

// List implements cache.Lister.
func (lw *ListerWatcher) List(opts metav1.ListOptions) (runtime.Object, error) {
	return lw.ListWithContext(context.Background(), opts)
}

// Watch implements cache.Watcher.
func (lw *ListerWatcher) Watch(opts metav1.ListOptions) (watch.Interface, error) {
	return lw.WatchWithContext(context.Background(), opts)
}

// WatchWithContext implements cache.WatcherWithContext: the feed after
// opts.ResourceVersion, or, asked to send its initial events, every object
// first and a bookmark that ends them.
func (lw *ListerWatcher) WatchWithContext(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	sel, err := lw.selection(opts)
	if err != nil {
		return nil, err
	}
	initial := opts.SendInitialEvents != nil && *opts.SendInitialEvents
	var cursor int64
	switch {
	case initial:
	case opts.ResourceVersion == "" || opts.ResourceVersion == "0":
		// From now.
		if cursor, err = lw.store.Head(ctx, lw.sch); err != nil {
			return nil, err
		}
	default:
		if cursor, err = strconv.ParseInt(opts.ResourceVersion, 10, 64); err != nil {
			return nil, apierrors.NewBadRequest(fmt.Sprintf("library: resourceVersion %q is not a sequence number", opts.ResourceVersion))
		}
		// An expired cursor is refused here, where a reflector expects it,
		// as well as by the first read.
		if _, _, err := lw.store.Changes(ctx, lw.sch, cursor); apierrors.IsResourceExpired(err) || apierrors.IsGone(err) {
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	w := &watcher{lw: lw, sel: sel, result: make(chan watch.Event), cancel: cancel, bookmarks: opts.AllowWatchBookmarks || initial}
	w.done.Add(1)
	go func() {
		defer w.done.Done()
		defer close(w.result)
		if initial {
			var ok bool
			if cursor, ok = w.sendInitial(ctx); !ok {
				return
			}
		}
		w.follow(ctx, cursor)
	}()
	return w, nil
}

// watcher is one watch of a kind.
type watcher struct {
	lw        *ListerWatcher
	sel       selection
	result    chan watch.Event
	cancel    context.CancelFunc
	done      sync.WaitGroup
	bookmarks bool
}

func (w *watcher) ResultChan() <-chan watch.Event { return w.result }

func (w *watcher) Stop() {
	w.cancel()
	// Drain, so the producer is never left blocked on a reader that left.
	go func() {
		for range w.result { //nolint:revive // draining
		}
	}()
	w.done.Wait()
}

func (w *watcher) send(ctx context.Context, e watch.Event) bool {
	select {
	case w.result <- e:
		return true
	case <-ctx.Done():
		return false
	}
}

func (w *watcher) fail(ctx context.Context, err error) {
	status := apierrors.NewInternalError(err).Status()
	var apiErr apierrors.APIStatus
	if errors.As(err, &apiErr) {
		status = apiErr.Status()
	}
	w.send(ctx, watch.Event{Type: watch.Error, Object: &status})
}

// bookmark is an object of the kind that carries a resourceVersion and
// nothing else, as the apiserver's bookmarks do.
func (w *watcher) bookmark(version int64, initialEnd bool) (runtime.Object, error) {
	obj := w.lw.newObject()
	if !w.lw.typed {
		obj.GetObjectKind().SetGroupVersionKind(w.lw.sch.GVK)
	}
	obj.SetResourceVersion(strconv.FormatInt(version, 10))
	if initialEnd {
		obj.SetAnnotations(map[string]string{metav1.InitialEventsAnnotationKey: "true"})
	}
	return obj, nil
}

// sendInitial streams every object of the kind as it stands in one
// snapshot, one row decoded at a time, then the bookmark that ends the
// initial events at the snapshot's sequence number.
func (w *watcher) sendInitial(ctx context.Context) (int64, bool) {
	sch := w.lw.sch
	tx, err := w.lw.store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		w.fail(ctx, fmt.Errorf("library: list %s: %w", sch.GVK.Kind, err))
		return 0, false
	}
	defer func() { _ = tx.Rollback() }()
	var head int64
	if err := tx.QueryRowContext(ctx, `SELECT seq FROM library.head WHERE kind = $1`, sch.GVK.Kind).Scan(&head); err != nil {
		w.fail(ctx, fmt.Errorf("library: list %s: %w", sch.GVK.Kind, err))
		return 0, false
	}
	managed := `managed`
	if w.lw.omitManaged {
		managed = `NULL::jsonb`
	}
	query := `SELECT namespace, name, version, ` + objectSQL("") + `, ` + managed + ` FROM library.` + sch.Table
	var args []any
	if w.lw.namespace != "" {
		query += ` WHERE namespace = $1`
		args = append(args, w.lw.namespace)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		w.fail(ctx, fmt.Errorf("library: list %s: %w", sch.GVK.Kind, err))
		return 0, false
	}
	defer func() { _ = rows.Close() }()
	read := 0
	defer func() { metrics.LibraryListRows.WithLabelValues(sch.GVK.Kind).Add(float64(read)) }()
	for rows.Next() {
		var raw Raw
		if err := rows.Scan(&raw.Namespace, &raw.Name, &raw.Version, &raw.Body, &raw.Managed); err != nil {
			w.fail(ctx, fmt.Errorf("library: list %s: %w", sch.GVK.Kind, err))
			return 0, false
		}
		read++
		if !w.emit(ctx, watch.Added, raw) {
			return 0, false
		}
	}
	if err := rows.Err(); err != nil {
		w.fail(ctx, fmt.Errorf("library: list %s: %w", sch.GVK.Kind, err))
		return 0, false
	}
	b, err := w.bookmark(head, true)
	if err != nil {
		w.fail(ctx, err)
		return 0, false
	}
	return head, w.send(ctx, watch.Event{Type: watch.Bookmark, Object: b})
}

// emit sends one object as an event, when the watch's selectors take it.
func (w *watcher) emit(ctx context.Context, typ watch.EventType, raw Raw) bool {
	obj, err := w.lw.object(raw)
	if err != nil {
		w.fail(ctx, err)
		return false
	}
	ok, err := w.sel.matches(w.lw.sch, raw, obj)
	if err != nil {
		w.fail(ctx, err)
		return false
	}
	if !ok {
		return true
	}
	return w.send(ctx, watch.Event{Type: typ, Object: obj})
}

// follow reads the feed from cursor until the watch stops: at each
// notification of a write to the kind, and at every poll interval.
func (w *watcher) follow(ctx context.Context, cursor int64) {
	store, sch := w.lw.store, w.lw.sch
	wake, stop := store.subscribe(sch.GVK.Kind)
	defer stop()
	poll := store.PollInterval
	if poll <= 0 {
		poll = DefaultPollInterval
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		for {
			changes, next, err := store.changes(ctx, sch, cursor, !w.lw.omitManaged)
			if err != nil {
				if ctx.Err() == nil {
					w.fail(ctx, err)
				}
				return
			}
			last := cursor
			for _, c := range changes {
				if w.lw.namespace != "" && c.Namespace != w.lw.namespace {
					continue
				}
				typ := watch.Modified
				switch c.Op {
				case 'A':
					typ = watch.Added
				case 'D':
					typ = watch.Deleted
				}
				if !w.emit(ctx, typ, c.Raw) {
					return
				}
				last = c.Version
			}
			if next > last && w.bookmarks {
				// The read reached past the last object sent: say so, and a
				// watch that resumes does not read the window again.
				b, err := w.bookmark(next, false)
				if err != nil {
					w.fail(ctx, err)
					return
				}
				if !w.send(ctx, watch.Event{Type: watch.Bookmark, Object: b}) {
					return
				}
			}
			if next == cursor {
				break // caught up
			}
			cursor = next
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-ticker.C:
		}
	}
}
