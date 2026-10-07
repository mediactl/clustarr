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

package metadataset

import (
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/app/catalog/controller/itemstatus"
)

// ErrConflict is an apply that lost its compare-and-swap: the caller
// requeues and renders the set from a fresh read.
var ErrConflict = errors.New("metadataset: the item changed since it was read")

// Apply is a reconciler's incorporation outside the loop -- the Artist's
// and the Author's (ADR-0019 A5.1 step 4): after its own catalog apply,
// which returned rv, it applies the set Build renders, unless the pass owes
// none or the cached item already holds it at exactly its paths.
func Apply(ctx context.Context, c client.Client, rv string, in Input) error {
	set, owed := Build(in)
	if !owed || itemstatus.SetEqual(in.Item, set) {
		return nil
	}
	_, conflicted, err := itemstatus.ApplySet(ctx, c, in.Item, rv, set)
	if err != nil {
		return fmt.Errorf("metadataset: apply %s/%s: %w", in.Item.GetNamespace(), in.Item.GetName(), err)
	}
	if conflicted {
		return ErrConflict
	}
	return nil
}
