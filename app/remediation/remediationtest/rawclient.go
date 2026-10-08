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

package remediationtest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"github.com/mediactl/clustarr/app/remediation"
)

// indexRecorder is a client.FieldIndexer that records each index's
// extractor by the indexed object's kind, so an uncached client can answer a
// field-selected List the way the manager's cache does.
type indexRecorder struct {
	scheme *runtime.Scheme
	mu     sync.Mutex
	byKind map[schema.GroupVersionKind]map[string]client.IndexerFunc
}

// IndexField implements client.FieldIndexer.
func (r *indexRecorder) IndexField(_ context.Context, obj client.Object, field string, extract client.IndexerFunc) error {
	gvk, err := apiutil.GVKForObject(obj, r.scheme)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byKind == nil {
		r.byKind = map[schema.GroupVersionKind]map[string]client.IndexerFunc{}
	}
	if r.byKind[gvk] == nil {
		r.byKind[gvk] = map[string]client.IndexerFunc{}
	}
	if _, dup := r.byKind[gvk][field]; dup {
		return fmt.Errorf("indexer conflict: %s %s", gvk.Kind, field)
	}
	r.byKind[gvk][field] = extract
	return nil
}

func (r *indexRecorder) extractor(gvk schema.GroupVersionKind, field string) (client.IndexerFunc, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.byKind[gvk][field]
	return f, ok
}

// rawClient is an uncached client whose List with a single exact-match
// field selector on a recorded index lists without the selector and keeps
// the items the index's extractor maps to the value, as the cache would. The
// apiserver itself selects on no custom field.
type rawClient struct {
	client.Client
	idx *indexRecorder
}

// NewRawClient wraps c, an uncached client, with every index
// remediation.RegisterIndexes registers: what the loop's passes, sources
// and actuators read through the manager's cache.
func NewRawClient(t testing.TB, c client.Client) client.Client {
	t.Helper()
	rec := &indexRecorder{scheme: c.Scheme()}
	if err := remediation.RegisterIndexes(context.Background(), rec); err != nil {
		t.Fatalf("record the loop's indexes: %v", err)
	}
	return &rawClient{Client: c, idx: rec}
}

// List implements client.Reader.
func (c *rawClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	lo := (&client.ListOptions{}).ApplyOptions(opts)
	if lo.FieldSelector == nil || lo.FieldSelector.Empty() {
		return c.Client.List(ctx, list, opts...)
	}
	reqs := lo.FieldSelector.Requirements()
	if len(reqs) != 1 || (reqs[0].Operator != selection.Equals && reqs[0].Operator != selection.DoubleEquals) {
		return c.Client.List(ctx, list, opts...)
	}
	gvk, err := apiutil.GVKForObject(list, c.Scheme())
	if err != nil {
		return err
	}
	gvk.Kind = strings.TrimSuffix(gvk.Kind, "List")
	extract, ok := c.idx.extractor(gvk, reqs[0].Field)
	if !ok {
		return c.Client.List(ctx, list, opts...)
	}
	lo.FieldSelector = nil
	if err := c.Client.List(ctx, list, lo); err != nil {
		return err
	}
	items, err := meta.ExtractList(list)
	if err != nil {
		return err
	}
	kept := items[:0]
	for _, it := range items {
		o, ok := it.(client.Object)
		if ok && slices.Contains(extract(o), reqs[0].Value) {
			kept = append(kept, it)
		}
	}
	return meta.SetList(list, kept)
}
