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
	"reflect"
	"strings"

	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/kube-openapi/pkg/validation/validate"
)

// rule is one x-kubernetes-validations rule, in Go. The apiserver evaluates
// the rule's CEL; the manager links no CEL runtime, so every rule a library
// kind's schema carries is written here once, keyed by its text, and a
// schema naming a rule this table lacks does not load (checkRules).
type rule struct {
	// transition marks a rule that reads oldSelf: it is evaluated only on an
	// update, and only where the old object has the value too.
	transition bool
	ok         func(self, oldSelf any) bool
}

var rules = map[string]rule{
	"self == oldSelf": {
		transition: true,
		ok:         reflect.DeepEqual,
	},
	"(has(self.magnetURL) ? 1 : 0) + (has(self.torrentURL) ? 1 : 0) + (has(self.nzbURL) ? 1 : 0) + (has(self.indexerDownload) ? 1 : 0) == 1": {
		ok: func(self, _ any) bool {
			n := 0
			for _, k := range []string{"magnetURL", "torrentURL", "nzbURL", "indexerDownload"} {
				if has(self, k) {
					n++
				}
			}
			return n == 1
		},
	},
	"has(self.authorRef) || (has(self.qualityProfileRef) && has(self.rootFolderRef))": {
		ok: func(self, _ any) bool {
			return has(self, "authorRef") || (has(self, "qualityProfileRef") && has(self, "rootFolderRef"))
		},
	},
	"self.kind == oldSelf.kind && self.name == oldSelf.name && has(self.keys) == has(oldSelf.keys) && (!has(self.keys) || self.keys == oldSelf.keys)": {
		transition: true,
		ok: func(self, oldSelf any) bool {
			return reflect.DeepEqual(get(self, "kind"), get(oldSelf, "kind")) &&
				reflect.DeepEqual(get(self, "name"), get(oldSelf, "name")) &&
				has(self, "keys") == has(oldSelf, "keys") &&
				(!has(self, "keys") || reflect.DeepEqual(get(self, "keys"), get(oldSelf, "keys")))
		},
	},
	"!has(self.track) || self.kind == 'album'": {
		ok: func(self, _ any) bool { return !has(self, "track") || get(self, "kind") == "album" },
	},
}

func has(x any, key string) bool {
	m, ok := x.(map[string]any)
	if !ok {
		return false
	}
	_, ok = m[key]
	return ok
}

func get(x any, key string) any {
	m, _ := x.(map[string]any)
	return m[key]
}

// ruleText is a rule as the table keys it: its whitespace folded.
func ruleText(s string) string { return strings.Join(strings.Fields(s), " ") }

// checkRules refuses a schema that carries a rule the table has no Go for.
func checkRules(s *structuralschema.Structural, kind string) error {
	var missing []string
	var walk func(s *structuralschema.Structural)
	walk = func(s *structuralschema.Structural) {
		if s == nil {
			return
		}
		for _, r := range s.XValidations {
			if _, ok := rules[ruleText(r.Rule)]; !ok {
				missing = append(missing, ruleText(r.Rule))
			}
		}
		for _, p := range s.Properties {
			walk(&p)
		}
		walk(s.Items)
		if s.AdditionalProperties != nil {
			walk(s.AdditionalProperties.Structural)
		}
	}
	walk(s)
	if len(missing) > 0 {
		return fmt.Errorf("library: %s: validation rules with no Go in pkg/library/validate.go: %q", kind, missing)
	}
	return nil
}

// validateRules evaluates every rule of the schema against obj, with old
// the object it replaces (nil on a create).
func validateRules(s *structuralschema.Structural, obj, old map[string]any) field.ErrorList {
	var errs field.ErrorList
	var walk func(s *structuralschema.Structural, p *field.Path, self, oldSelf any, hasOld bool)
	walk = func(s *structuralschema.Structural, p *field.Path, self, oldSelf any, hasOld bool) {
		if s == nil || self == nil {
			return
		}
		for _, r := range s.XValidations {
			impl := rules[ruleText(r.Rule)]
			if impl.transition && !hasOld {
				continue
			}
			if !impl.ok(self, oldSelf) {
				errs = append(errs, field.Invalid(p, typeName(self), r.Message))
			}
		}
		switch v := self.(type) {
		case map[string]any:
			oldMap, _ := oldSelf.(map[string]any)
			for k, child := range v {
				oldChild, ok := oldMap[k]
				ok = ok && hasOld
				if prop, found := s.Properties[k]; found {
					walk(&prop, p.Child(k), child, oldChild, ok)
				} else if s.AdditionalProperties != nil {
					walk(s.AdditionalProperties.Structural, p.Key(k), child, oldChild, ok)
				}
			}
		case []any:
			oldList, _ := oldSelf.([]any)
			for i, child := range v {
				oldChild, ok := correlate(s, child, oldList)
				walk(s.Items, p.Index(i), child, oldChild, ok && hasOld)
			}
		}
	}
	walk(s, nil, obj, old, old != nil)
	return errs
}

// correlate finds the old item an item of a list replaces: the one with the
// same keys in a map list. An item of any other list has no old self.
func correlate(s *structuralschema.Structural, item any, old []any) (any, bool) {
	if s.XListType == nil || *s.XListType != "map" || len(s.XListMapKeys) == 0 {
		return nil, false
	}
	m, ok := item.(map[string]any)
	if !ok {
		return nil, false
	}
	for _, o := range old {
		om, ok := o.(map[string]any)
		if !ok {
			continue
		}
		same := true
		for _, k := range s.XListMapKeys {
			if !reflect.DeepEqual(m[k], om[k]) {
				same = false
				break
			}
		}
		if same {
			return o, true
		}
	}
	return nil, false
}

func typeName(x any) string {
	switch x.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	default:
		return "number"
	}
}

// validateValues runs a schema validator and turns its findings into field
// errors.
func validateValues(v *validate.SchemaValidator, p *field.Path, x any) field.ErrorList {
	if v == nil {
		return nil
	}
	var errs field.ErrorList
	for _, e := range v.Validate(x).Errors {
		errs = append(errs, field.Invalid(p, "", e.Error()))
	}
	return errs
}
