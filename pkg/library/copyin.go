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
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// CopyIn writes an object read from an apiserver as the row the store would
// hold for it: its body without its status, its resourceVersion and its
// ownership records, and those two in their own columns. Nothing is
// defaulted, validated or recomputed: its uid, creation time, generation,
// finalizers and owners stay, and its ownership records are the
// apiserver's, so the next apply to it merges with what was recorded there.
// The trigger gives it a version. A row already there is left alone, and
// the answer is whether one was written.
//
// It is the cutover's (hack/librarycopy, docs/runbooks/
// library-store-cutover.md) and goes when that tool does: nothing else may
// write a row this way.
func (s *Store) CopyIn(ctx context.Context, sch *Schema, u *unstructured.Unstructured) (bool, error) {
	content := u.DeepCopy().Object
	managed, _, _ := unstructured.NestedSlice(content, "metadata", "managedFields")
	unstructured.RemoveNestedField(content, "metadata", "managedFields")
	unstructured.RemoveNestedField(content, "metadata", "resourceVersion")
	status, hasStatus := content["status"]
	delete(content, "status")
	body, err := json.Marshal(content)
	if err != nil {
		return false, err
	}
	var statusArg any
	if hasStatus && status != nil {
		raw, err := json.Marshal(status)
		if err != nil {
			return false, err
		}
		statusArg = string(raw)
	}
	if managed == nil {
		managed = []any{}
	}
	owned, err := json.Marshal(managed)
	if err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO library.`+sch.Table+` (namespace, name, body, status, managed)
		 VALUES ($1, $2, $3::jsonb, $4::jsonb, $5::jsonb) ON CONFLICT (namespace, name) DO NOTHING`,
		u.GetNamespace(), u.GetName(), string(body), statusArg, string(owned))
	if err != nil {
		return false, fmt.Errorf("library: copy %s %s/%s in: %w", sch.GVK.Kind, u.GetNamespace(), u.GetName(), err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// Check reports what the store would refuse a write of obj for: the values
// its schema does not take, in the whole object and in its status alone (a
// status write is held to the status), and the kind's rules that hold of an
// object by itself. It is how the cutover learns, before the manager does,
// of a copied object the apiserver served and the store would not let
// change.
func (sch *Schema) Check(obj map[string]any) field.ErrorList {
	errs := validateValues(sch.validator, nil, obj)
	errs = append(errs, validateValues(sch.statusValidator, field.NewPath("status"), obj["status"])...)
	return append(errs, validateRules(sch.structural, obj, nil)...)
}
