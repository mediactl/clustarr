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

import (
	"encoding/json"
	"fmt"
)

// ApplyConfigurationsFrom converts API values into the generated apply
// configuration of the same schema, for a writer that holds typed values (a
// list read back from the live object, or built by a pure function) and must
// declare them through a generated With* builder that takes apply
// configurations.
//
// The conversion is a JSON round trip, deliberately rather than a
// hand-written field-by-field copy. A generated apply configuration has the
// same JSON shape as its API type, so the round trip carries every leaf the
// value carries -- including a field added to the API type later, which a
// hand-written copy would silently stop declaring and so release under
// server-side apply. It is also byte-exact with what marshalling the API value
// itself would send: a zero leaf the API type tags omitempty stays absent, and
// is not turned into an explicit zero that would claim the leaf.
//
// AC is the apply-configuration type and T the API type; T is inferred:
//
//	acs, err := k8s.ApplyConfigurationsFrom[catalogac.IndexerOutcomeApplyConfiguration](outcomes)
//
// A nil or empty input yields an empty, non-nil result, which a variadic
// With* call spreads into nothing.
func ApplyConfigurationsFrom[AC any, T any](items []T) ([]*AC, error) {
	out := make([]*AC, 0, len(items))
	for i := range items {
		raw, err := json.Marshal(items[i])
		if err != nil {
			return nil, fmt.Errorf("marshal %T at index %d: %w", items[i], i, err)
		}
		ac := new(AC)
		if err := json.Unmarshal(raw, ac); err != nil {
			return nil, fmt.Errorf("unmarshal %T into %T at index %d: %w", items[i], ac, i, err)
		}
		out = append(out, ac)
	}
	return out, nil
}
