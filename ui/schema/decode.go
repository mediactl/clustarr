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

package schema

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Decode reads a posted form into a spec object by the tree under root.
// Input names are field paths with array indices filled in
// ("usenet.providers.0.host"); a map takes paired "<path>.__k.<i>" and
// "<path>.__v.<i>" inputs; an array of scalars takes repeated inputs of
// one name, or one input with a value per line. Every value is typed by
// its field: an integer or number parsed, a boolean read from the input's
// last value (so a hidden false under a checkbox true reads true), an enum
// held to the schema. An empty input is omitted, as is an empty object or
// row, and a name the schema does not know is ignored. The first bad value
// is returned as an error naming the field.
func Decode(root *Field, values url.Values) (map[string]any, error) {
	v, err := decodeObject(root, "", values)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return map[string]any{}, nil
	}
	return v, nil
}

func decodeObject(f *Field, base string, values url.Values) (map[string]any, error) {
	out := map[string]any{}
	for _, c := range f.Fields {
		v, err := decodeField(c, join(base, c.Name), values)
		if err != nil {
			return nil, err
		}
		if v != nil {
			out[c.Name] = v
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// decodeField returns nil for an absent or empty input.
func decodeField(f *Field, name string, values url.Values) (any, error) {
	switch f.Type {
	case TypeObject:
		return orNil(decodeObject(f, name, values))
	case TypeMap:
		return decodeMap(f, name, values)
	case TypeArray:
		return decodeArray(f, name, values)
	default:
		raw, ok := last(values, name)
		if !ok {
			return nil, nil
		}
		return scalar(f, name, raw)
	}
}

func orNil(m map[string]any, err error) (any, error) {
	if err != nil || m == nil {
		return nil, err
	}
	return m, nil
}

// last is the input's last non-empty value, trimmed; false when the input
// is absent or blank.
func last(values url.Values, name string) (string, bool) {
	vs, ok := values[name]
	if !ok {
		return "", false
	}
	for i := len(vs) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(vs[i]); s != "" {
			return s, true
		}
	}
	return "", false
}

func scalar(f *Field, name, raw string) (any, error) {
	switch f.Type {
	case TypeInteger:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not an integer", name, raw)
		}
		return n, nil
	case TypeNumber:
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a number", name, raw)
		}
		return n, nil
	case TypeBoolean:
		switch strings.ToLower(raw) {
		case "true", "on", "1", "yes":
			return true, nil
		case "false", "off", "0", "no":
			return false, nil
		}
		return nil, fmt.Errorf("%s: %q is not a boolean", name, raw)
	default:
		if len(f.Enum) > 0 {
			for _, e := range f.Enum {
				if e == raw {
					return raw, nil
				}
			}
			return nil, fmt.Errorf("%s: %q is not one of %s", name, raw, strings.Join(f.Enum, ", "))
		}
		return raw, nil
	}
}

func decodeMap(f *Field, name string, values url.Values) (any, error) {
	out := map[string]any{}
	for i := range indices(values, name+".__k.") {
		key, ok := last(values, fmt.Sprintf("%s.__k.%d", name, i))
		if !ok {
			continue
		}
		raw, ok := last(values, fmt.Sprintf("%s.__v.%d", name, i))
		if !ok {
			continue
		}
		v, err := scalar(f.Values, name+"."+key, raw)
		if err != nil {
			return nil, err
		}
		out[key] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func decodeArray(f *Field, name string, values url.Values) (any, error) {
	if f.Items.Type == TypeObject {
		var out []any
		for _, i := range indices(values, name+".") {
			el, err := decodeObject(f.Items, fmt.Sprintf("%s.%d", name, i), values)
			if err != nil {
				return nil, err
			}
			if el != nil {
				out = append(out, el)
			}
		}
		if len(out) == 0 {
			return nil, nil
		}
		return out, nil
	}
	var out []any
	for _, raw := range values[name] {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			v, err := scalar(f.Items, name, line)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// indices lists, in order, every index i for which some input is named
// "<prefix><i>" or "<prefix><i>.…".
func indices(values url.Values, prefix string) []int {
	seen := map[int]bool{}
	for k := range values {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := k[len(prefix):]
		if j := strings.IndexByte(rest, '.'); j >= 0 {
			rest = rest[:j]
		}
		if n, err := strconv.Atoi(rest); err == nil {
			seen[n] = true
		}
	}
	out := make([]int, 0, len(seen))
	for i := range seen {
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}
