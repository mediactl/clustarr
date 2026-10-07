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

package k8s

import "sigs.k8s.io/controller-runtime/pkg/client"

// FieldIndex is one cache field index a component reads, declared rather
// than registered (spec §3.5.2 step 9). A field index name is global to a
// cache, and a second IndexField of one name on one kind is an error, so
// only the process registers the indexes its components declare: once, and
// then it proves each reached the cache (internal/cli/agent's
// registerIndexes, then app/catalog/agent.AssertIndexes). A component that registered its own would make the
// process's other readers of the same index an ordering accident.
type FieldIndex struct {
	// Object is an empty object of the indexed kind, as IndexField takes it.
	Object client.Object
	// Name is the index name the readers' client.MatchingFields uses.
	Name string
	// Extract is the index's value function.
	Extract client.IndexerFunc
}
