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

package library_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	toolscache "k8s.io/client-go/tools/cache"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/library"
	"github.com/mediactl/clustarr/test/librarytest"
)

func issueSchema(t *testing.T, st *library.Store) *library.Schema {
	t.Helper()
	sch := st.Schemas().For(catalogv1.GroupVersion.WithKind("Issue"))
	require.NotNil(t, sch, "Issue is a library kind")
	return sch
}

func newIssue(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "catalog.clustarr.io/v1alpha1", "kind": "Issue",
		"metadata": map[string]any{"name": name, "namespace": "default"},
		"spec":     map[string]any{"comicRef": "saga", "number": "1", "calculatedNumberCentis": int64(100)},
	}}
}

func catalogScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1.AddToScheme(scheme))
	return scheme
}

// TestTheFeedIsInCommitOrder is design §4, item 1. A feed read by a cursor
// loses a change when a write with a lower sequence number commits after one
// with a higher: the reader has passed it. Here it cannot happen: a write
// takes its number from its kind's head row and holds that row until it
// commits, so a second write waits for the first and a reader never sees a
// number with an unseen one below it. The test holds a write open, shows
// that another neither commits nor appears to a reader while it is, and that
// both arrive in order once it ends. With the head row's lock taken out of
// the trigger (a sequence in its place) the second write commits first and
// the assertion that it is still waiting fails.
func TestTheFeedIsInCommitOrder(t *testing.T) {
	st := librarytest.Start(t).Store(t, nil)
	sch := issueSchema(t, st)
	ctx := t.Context()
	for _, n := range []string{"a", "b"} {
		_, err := st.Create(ctx, newIssue(n), "clustarr")
		require.NoError(t, err)
	}
	_, cursor, err := st.Changes(ctx, sch, 0)
	require.NoError(t, err)

	// The first write: made, not committed.
	held, err := st.DB().BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = held.Rollback() }()
	_, err = held.ExecContext(ctx, `UPDATE library.issue SET body = jsonb_set(body, '{metadata,labels}', '{"n":"1"}') WHERE name = 'a'`)
	require.NoError(t, err)

	// The second, to another object, by the store.
	second := make(chan error, 1)
	go func() {
		_, err := st.Patch(context.WithoutCancel(ctx), sch, "default", "b", []byte(`{"metadata":{"labels":{"n":"2"}}}`), "clustarr-ui", false)
		second <- err
	}()
	select {
	case err := <-second:
		t.Fatalf("the second write committed while the first was open (err %v): its sequence number is above one a reader has not seen", err)
	case <-time.After(500 * time.Millisecond):
	}
	changes, next, err := st.Changes(ctx, sch, cursor)
	require.NoError(t, err)
	assert.Empty(t, changes, "nothing has committed")
	assert.Equal(t, cursor, next)

	require.NoError(t, held.Commit())
	require.NoError(t, <-second)
	changes, next, err = st.Changes(ctx, sch, cursor)
	require.NoError(t, err)
	require.Len(t, changes, 2)
	assert.Equal(t, []string{"a", "b"}, []string{changes[0].Name, changes[1].Name}, "in the order they committed")
	assert.Equal(t, cursor+2, next)
	assert.Less(t, changes[0].Version, changes[1].Version)
}

// TestAnInformerOnTheStoreMissesNothing runs a client-go informer on the
// store's list-watcher while eight writers create, patch, apply to and
// delete forty objects, and the feed is trimmed under it so that it must
// list again: when the writers stop, the informer holds exactly what the
// table does, at the same versions.
func TestAnInformerOnTheStoreMissesNothing(t *testing.T) {
	st := librarytest.Start(t).Store(t, nil)
	st.PollInterval = 200 * time.Millisecond
	sch := issueSchema(t, st)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lw, err := st.ListerWatcher(sch, catalogScheme(t), "")
	require.NoError(t, err)
	store, informer := toolscache.NewInformerWithOptions(toolscache.InformerOptions{
		ListerWatcher: lw, ObjectType: &catalogv1.Issue{}, Handler: toolscache.ResourceEventHandlerFuncs{},
	})
	go informer.RunWithContext(ctx)
	require.True(t, toolscache.WaitForCacheSync(ctx.Done(), informer.HasSynced))

	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(w), 7))
			for i := range 150 {
				name := fmt.Sprintf("issue-%02d", rng.IntN(40))
				var err error
				switch rng.IntN(5) {
				case 0:
					_, err = st.Create(ctx, newIssue(name), "clustarr")
				case 1:
					err = st.Delete(ctx, sch, "default", name, library.Preconditions{})
				case 2:
					_, err = st.Patch(ctx, sch, "default", name, []byte(fmt.Sprintf(`{"metadata":{"labels":{"n":"%d-%d"}}}`, w, i)), "clustarr-ui", false)
				default:
					applied := newIssue(name)
					delete(applied.Object, "spec")
					applied.Object["status"] = map[string]any{"title": fmt.Sprintf("%d-%d", w, i)}
					_, err = st.Apply(ctx, applied, fmt.Sprintf("writer-%d", w), true, true)
				}
				if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsAlreadyExists(err) {
					t.Errorf("writer %d: %v", w, err)
					return
				}
				if w == 0 && i%50 == 49 {
					// Everything so far is trimmed: a watch behind it lists again.
					if err := st.Trim(ctx, 0); err != nil {
						t.Errorf("trim: %v", err)
					}
				}
			}
		}()
	}
	wg.Wait()

	want, _, err := st.ListRaw(ctx, sch, "")
	require.NoError(t, err)
	held := func() map[string]string {
		out := map[string]string{}
		for _, o := range store.List() {
			iss := o.(*catalogv1.Issue)
			out[iss.Name] = iss.ResourceVersion + " " + iss.Status.Title + " " + iss.Labels["n"]
		}
		return out
	}
	wantMap := map[string]string{}
	for _, raw := range want {
		var iss catalogv1.Issue
		require.NoError(t, json.Unmarshal(raw.Body, &iss))
		wantMap[raw.Name] = strconv.FormatInt(raw.Version, 10) + " " + iss.Status.Title + " " + iss.Labels["n"]
	}
	require.NotEmpty(t, wantMap)
	require.EventuallyWithT(t, func(c *assert.CollectT) { assert.Equal(c, wantMap, held()) }, 10*time.Second, 50*time.Millisecond)
}

// TestAWatchSendsItsInitialEventsThenFollows is the streamed initial list a
// reflector asks for (and the manager's row informer starts from): every
// object as an Added event, a bookmark that ends them at the snapshot's
// sequence number, then the changes after it.
func TestAWatchSendsItsInitialEventsThenFollows(t *testing.T) {
	st := librarytest.Start(t).Store(t, nil)
	st.PollInterval = 200 * time.Millisecond
	sch := issueSchema(t, st)
	ctx := t.Context()
	for _, n := range []string{"a", "b", "c"} {
		_, err := st.Create(ctx, newIssue(n), "clustarr")
		require.NoError(t, err)
	}
	lw, err := st.ListerWatcher(sch, catalogScheme(t), "")
	require.NoError(t, err)
	yes := true
	w, err := lw.WatchWithContext(ctx, metav1.ListOptions{SendInitialEvents: &yes, AllowWatchBookmarks: true, ResourceVersionMatch: metav1.ResourceVersionMatchNotOlderThan})
	require.NoError(t, err)
	defer w.Stop()

	next := func() watch.Event {
		select {
		case e, ok := <-w.ResultChan():
			require.True(t, ok, "the watch ended")
			return e
		case <-time.After(10 * time.Second):
			t.Fatal("no event")
			return watch.Event{}
		}
	}
	seen := map[string]bool{}
	for range 3 {
		e := next()
		require.Equal(t, watch.Added, e.Type)
		seen[e.Object.(*catalogv1.Issue).Name] = true
	}
	assert.Equal(t, map[string]bool{"a": true, "b": true, "c": true}, seen)
	end := next()
	require.Equal(t, watch.Bookmark, end.Type)
	mark := end.Object.(*catalogv1.Issue)
	assert.Equal(t, "true", mark.Annotations[metav1.InitialEventsAnnotationKey])
	assert.Equal(t, "3", mark.ResourceVersion)

	require.NoError(t, st.Delete(ctx, sch, "default", "b", library.Preconditions{}))
	gone := next()
	require.Equal(t, watch.Deleted, gone.Type)
	assert.Equal(t, "b", gone.Object.(*catalogv1.Issue).Name)
	assert.Equal(t, "4", gone.Object.(*catalogv1.Issue).ResourceVersion, "a deleted object carries the delete's sequence number")
}
