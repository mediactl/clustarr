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

package agent

import (
	"context"
	"fmt"
	"reflect"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/k8s"
)

type indexKey struct {
	kind reflect.Type
	name string
}

// registerIndexes registers each index the domain declared on idx, once
// (spec §3.5.2 step 9). It refuses an incomplete declaration and one
// (kind, name) declared twice before it registers anything: IndexField on an
// existing (kind, name) is an "indexer conflict", and a half-registered set
// is a degraded reader. The agent is the one registrar of declared indexes
// in code a binary links (TestTheAgentIsTheOnlyIndexRegistrar); a test that
// builds its manager by hand registers through test/starttest.
func registerIndexes(ctx context.Context, idx client.FieldIndexer, ix []k8s.FieldIndex) error {
	seen := make(map[indexKey]bool, len(ix))
	for _, fi := range ix {
		if fi.Object == nil || fi.Name == "" || fi.Extract == nil {
			return fmt.Errorf("agent: field index %q on %T: incomplete declaration", fi.Name, fi.Object)
		}
		k := indexKey{reflect.TypeOf(fi.Object), fi.Name}
		if seen[k] {
			return fmt.Errorf("agent: field index %q on %T is declared twice", fi.Name, fi.Object)
		}
		seen[k] = true
	}
	for _, fi := range ix {
		if err := idx.IndexField(ctx, fi.Object, fi.Name, fi.Extract); err != nil {
			return fmt.Errorf("agent: register field index %q on %T: %w", fi.Name, fi.Object, err)
		}
	}
	return nil
}
