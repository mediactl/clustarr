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

package starttest

import (
	"context"
	"fmt"
	"reflect"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// RegisterIndexes registers each declared index on idx once, as
// internal/cli/agent does, for a test that builds its manager by hand. It
// lives here, under test/, because only the processes may call IndexField
// in code a binary links. A (kind, name) declared by two slices is
// registered once.
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer, ix ...[]k8s.FieldIndex) error {
	type key struct {
		kind reflect.Type
		name string
	}
	seen := map[key]bool{}
	for _, set := range ix {
		for _, fi := range set {
			k := key{reflect.TypeOf(fi.Object), fi.Name}
			if seen[k] {
				continue
			}
			seen[k] = true
			if err := idx.IndexField(ctx, fi.Object, fi.Name, fi.Extract); err != nil {
				return fmt.Errorf("starttest: field index %q on %T: %w", fi.Name, fi.Object, err)
			}
		}
	}
	return nil
}
