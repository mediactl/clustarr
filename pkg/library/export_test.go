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
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
)

// ListConditions is the WHERE a list with these selectors sends to the
// database, with its arguments: what a test explains to see the index used.
func (s *Schema) ListConditions(field fields.Selector, label labels.Selector) (conds []string, args []any) {
	var a sqlArgs
	conds = s.narrow(selection{labels: label, fields: field}, &a)
	return conds, a.args
}

// SelectableIndexNames are the names of the indexes a migration makes for
// the kind's selectable fields, by the field's path.
func (s *Schema) SelectableIndexNames() map[string]string {
	out := map[string]string{}
	for _, ix := range s.selectableIndexes() {
		for path, expr := range s.fieldSQL {
			if expr == ix.expr {
				out[path] = ix.name
			}
		}
	}
	return out
}
