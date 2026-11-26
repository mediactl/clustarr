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
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	utiljson "k8s.io/apimachinery/pkg/util/json"
)

// checkFieldSelector refuses a field selector naming a field the kind does
// not offer: metadata.name, metadata.namespace, and the CRD's selectable
// fields, as the apiserver does.
func (s *Schema) checkFieldSelector(sel fields.Selector) error {
	for _, r := range sel.Requirements() {
		if r.Field == "metadata.name" || r.Field == "metadata.namespace" || slices.Contains(s.SelectableFields, r.Field) {
			continue
		}
		return apierrors.NewBadRequest(fmt.Sprintf("field label not supported: %s", r.Field))
	}
	return nil
}

// fieldSet is the values of an object's selectable fields. An absent field
// is the empty string, as the apiserver has it.
func (s *Schema) fieldSet(raw Raw) (fields.Set, error) {
	set := fields.Set{"metadata.name": raw.Name, "metadata.namespace": raw.Namespace}
	if len(s.SelectableFields) == 0 {
		return set, nil
	}
	var content map[string]any
	if err := utiljson.Unmarshal(raw.Body, &content); err != nil {
		return nil, fmt.Errorf("library: decode %s/%s: %w", raw.Namespace, raw.Name, err)
	}
	for _, path := range s.SelectableFields {
		var v any = content
		for _, seg := range strings.Split(path, ".") {
			m, _ := v.(map[string]any)
			v = m[seg]
		}
		if v == nil {
			set[path] = ""
			continue
		}
		set[path] = fmt.Sprint(v)
	}
	return set, nil
}
