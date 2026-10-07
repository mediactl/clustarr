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

package artwork

import (
	"context"

	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/source"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/objindex"
)

// IndexSource is S26 (ADR-0019 §8.1): a controller source over the
// manager's artwork index that enqueues, for every change, the item the
// changed object's metadata names -- an original fetched, backfilled or
// lost, an overlay drawn or removed. A change whose object names no item is
// skipped; a lost wake costs latency, never correctness.
type IndexSource[K comparable] struct {
	Index *objindex.Index
	// ToKey maps an object's item to the controller's key; false skips it.
	ToKey func(kind commonv1.MediaKind, namespace, name string) (K, bool)
}

var _ source.TypedSource[string] = (*IndexSource[string])(nil)

// Start implements source.TypedSource: it subscribes and forwards until
// ctx ends.
func (s *IndexSource[K]) Start(ctx context.Context, q workqueue.TypedRateLimitingInterface[K]) error {
	ch, cancel := s.Index.Subscribe()
	go func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case c, ok := <-ch:
				if !ok {
					return
				}
				kind, ns, name, ok := ItemOfMeta(c.Metadata)
				if !ok {
					continue
				}
				if k, ok := s.ToKey(kind, ns, name); ok {
					q.Add(k)
				}
			}
		}
	}()
	return nil
}

// String implements fmt.Stringer for the controller's logs.
func (s *IndexSource[K]) String() string { return "artwork index source" }
