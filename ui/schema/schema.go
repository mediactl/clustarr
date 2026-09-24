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

// Package schema turns a CRD's spec schema into the field tree the
// Settings forms render from, decodes a posted form back into a spec by
// that tree, and computes the merge patch an edit sends (Diff) (settings CRUD
// design, 2026-09-24). The schema is the one the apiserver enforces
// (config/crd/bases, embedded), so a new API field reaches the forms
// without anyone hand-writing it; the per-kind overlay in ui/settings only
// arranges what this package finds.
package schema

import (
	"encoding/json"
	"sort"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// Type is a field's kind as a form sees it: the JSON type, plus map for a
// string-keyed object with additionalProperties and quantity for
// Kubernetes' int-or-string resource quantities.
type Type string

const (
	TypeString   Type = "string"
	TypeInteger  Type = "integer"
	TypeNumber   Type = "number"
	TypeBoolean  Type = "boolean"
	TypeObject   Type = "object"
	TypeArray    Type = "array"
	TypeMap      Type = "map"
	TypeQuantity Type = "quantity"
)

// Field is one node of the tree: a scalar, an object with Fields, an array
// with Items, or a map with Values.
type Field struct {
	// Path is the dotted path from spec, "" for spec itself; an array
	// item's children carry "[]" for the index ("usenet.providers[].host").
	// Form input names are the path with the index filled in
	// ("usenet.providers.0.host").
	Path string
	// Name is the last path segment.
	Name        string
	Type        Type
	Description string
	// Required is whether the parent object's required list names it.
	Required bool
	Enum     []string
	// Default is the schema default decoded as JSON does (numbers are
	// float64), nil when none.
	Default          any
	Minimum, Maximum *float64
	Pattern          string
	Format           string
	// Fields are an object's children, sorted by name.
	Fields []*Field
	// Items is an array's element.
	Items *Field
	// Values is a map's value.
	Values *Field
}

// Walk builds the tree of spec.
func Walk(spec *apiextensionsv1.JSONSchemaProps) *Field {
	return walk("", "", spec, false)
}

func walk(path, name string, s *apiextensionsv1.JSONSchemaProps, required bool) *Field {
	f := &Field{Path: path, Name: name, Description: s.Description, Required: required, Pattern: s.Pattern, Format: s.Format, Minimum: s.Minimum, Maximum: s.Maximum}
	if s.Default != nil {
		_ = json.Unmarshal(s.Default.Raw, &f.Default)
	}
	for _, e := range s.Enum {
		var v any
		if json.Unmarshal(e.Raw, &v) == nil {
			if str, ok := v.(string); ok {
				f.Enum = append(f.Enum, str)
			}
		}
	}
	switch {
	case s.XIntOrString:
		f.Type = TypeQuantity
	case s.Type == "object" && s.AdditionalProperties != nil:
		f.Type = TypeMap
		if s.AdditionalProperties.Schema != nil {
			f.Values = walk(path+"{}", "", s.AdditionalProperties.Schema, false)
		} else {
			f.Values = &Field{Path: path + "{}", Type: TypeString}
		}
	case s.Type == "object":
		f.Type = TypeObject
		req := map[string]bool{}
		for _, r := range s.Required {
			req[r] = true
		}
		names := make([]string, 0, len(s.Properties))
		for n := range s.Properties {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			child := s.Properties[n]
			f.Fields = append(f.Fields, walk(join(path, n), n, &child, req[n]))
		}
	case s.Type == "array":
		f.Type = TypeArray
		if s.Items != nil && s.Items.Schema != nil {
			f.Items = walk(path+"[]", "", s.Items.Schema, false)
		} else {
			f.Items = &Field{Path: path + "[]", Type: TypeString}
		}
	default:
		f.Type = Type(s.Type)
		if f.Type == "" {
			f.Type = TypeString
		}
	}
	return f
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// Lookup finds the field at a dotted path below f: object children by
// name, an array's item's children through "[]" or a bare index
// ("usenet.providers[].host" and "usenet.providers.0.host" both reach the
// same field). nil when there is none.
func (f *Field) Lookup(path string) *Field {
	if path == "" {
		return f
	}
	cur := f
	for _, seg := range strings.Split(path, ".") {
		next := cur.child(seg)
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

// child resolves one path segment: "name", "name[]" or, on an array, an
// index.
func (f *Field) child(seg string) *Field {
	if f.Type == TypeArray {
		if isIndex(seg) || seg == "[]" {
			return f.Items
		}
		return nil
	}
	if f.Type != TypeObject {
		return nil
	}
	name := strings.TrimSuffix(seg, "[]")
	for _, c := range f.Fields {
		if c.Name == name {
			if name != seg {
				// "providers[]" reaches the item itself.
				if c.Type != TypeArray {
					return nil
				}
				return c.Items
			}
			return c
		}
	}
	return nil
}

func isIndex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
