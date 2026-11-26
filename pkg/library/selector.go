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
	"hash/fnv"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	op "k8s.io/apimachinery/pkg/selection"
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

// plainSegment is a path segment that can stand in a Postgres text-array
// literal as it is.
var plainSegment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// fieldExpr is the SQL expression that reads a selectable field of a row as
// the field selector compares it: the field's text, and the empty string
// when it is absent (fieldSet). It is the expression of the field's index
// too, to the letter, which is what lets the planner use the index. Only a
// string, boolean or integer field of plain segments has one: the database
// writes those as fieldSet does ("Running", "true", "42"). A number with a
// fraction it may not (api/ has none), and a field of any other shape is
// filtered here, after the read, as every field was before.
func fieldExpr(structural *structuralschema.Structural, path string) (string, bool) {
	segs := strings.Split(path, ".")
	at := structural
	for _, seg := range segs {
		if at == nil || !plainSegment.MatchString(seg) {
			return "", false
		}
		next, ok := at.Properties[seg]
		if !ok {
			return "", false
		}
		at = &next
	}
	switch at.Type {
	case "string", "boolean", "integer":
	default:
		return "", false
	}
	// The row keeps status in a column of its own (kindDDL).
	column := "body"
	if segs[0] == "status" {
		column, segs = "status", segs[1:]
		if len(segs) == 0 {
			return "", false
		}
	}
	return `COALESCE(` + column + ` #>> '{` + strings.Join(segs, ",") + `}', '')`, true
}

// selectableIndex is one selectable field's index: its name and what it
// indexes.
type selectableIndex struct{ name, expr string }

// selectableIndexes are the indexes a migration makes for the kind: one per
// selectable field the database can filter by (fieldExpr), in the order of
// their paths. A list by such a field reads the rows that match and no
// other; without the index it reads every row's status to find them.
func (s *Schema) selectableIndexes() []selectableIndex {
	paths := make([]string, 0, len(s.fieldSQL))
	for path := range s.fieldSQL {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	out := make([]selectableIndex, 0, len(paths))
	for _, path := range paths {
		name := s.Table + "_sel_" + strings.ToLower(strings.ReplaceAll(path, ".", "_"))
		// Postgres cuts an identifier at 63 bytes; two long paths must not
		// meet in one name.
		if len(name) > 63 {
			h := fnv.New32a()
			_, _ = h.Write([]byte(path))
			name = name[:54] + "_" + strconv.FormatUint(uint64(h.Sum32()), 16)
		}
		out = append(out, selectableIndex{name: name, expr: s.fieldSQL[path]})
	}
	return out
}

// sqlArgs numbers a statement's arguments.
type sqlArgs struct{ args []any }

func (a *sqlArgs) add(v any) string {
	a.args = append(a.args, v)
	return "$" + strconv.Itoa(len(a.args))
}

// narrow is the part of a selection the database applies, as conditions to
// AND into a WHERE. It only ever narrows: every row the database returns
// still passes sel.matches, the one authority on what a selector means, so
// a requirement with no condition here (a negated label, a field that is
// not a scalar) costs a decode of the rows it would have excluded and nothing
// else. What it must never do is exclude a row matches would keep, and each
// condition below is the requirement it stands for, exactly: a field is its
// text or the empty string (fieldExpr), a label is a string that is there
// or is not.
func (s *Schema) narrow(sel selection, a *sqlArgs) []string {
	var conds []string
	if sel.fields != nil {
		for _, r := range sel.fields.Requirements() {
			var expr string
			switch r.Field {
			case "metadata.name":
				expr = "name"
			case "metadata.namespace":
				expr = "namespace"
			default:
				var ok bool
				if expr, ok = s.fieldSQL[r.Field]; !ok {
					continue
				}
			}
			switch r.Operator {
			case op.Equals, op.DoubleEquals:
				conds = append(conds, expr+" = "+a.add(r.Value))
			case op.NotEquals:
				conds = append(conds, expr+" <> "+a.add(r.Value))
			}
		}
	}
	if sel.labels != nil {
		reqs, _ := sel.labels.Requirements()
		for _, r := range reqs {
			label := `(body -> 'metadata' -> 'labels' ->> ` + a.add(r.Key()) + `::text)`
			switch r.Operator() {
			case op.Equals, op.DoubleEquals, op.In:
				values := r.Values().List()
				if len(values) == 0 {
					continue
				}
				marks := make([]string, len(values))
				for i, v := range values {
					marks[i] = a.add(v)
				}
				conds = append(conds, label+" IN ("+strings.Join(marks, ", ")+")")
			case op.Exists:
				conds = append(conds, label+" IS NOT NULL")
			default:
				// The key was numbered for nothing; a statement may not
				// carry an argument it does not use.
				a.args = a.args[:len(a.args)-1]
			}
		}
	}
	return conds
}
