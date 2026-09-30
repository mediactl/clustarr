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

package plex

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// customization is Plex's Response Customization (research §3; PMS API
// "Response Customization"): includeFields/excludeFields name scalar
// attributes, includeElements/excludeElements name the arrays and objects
// (Image, Role, Children, …). Plex sends them on match requests to keep
// responses small.
type customization struct {
	includeFields, excludeFields, includeElements, excludeElements map[string]bool
}

// identityFields are never removed: PMS cannot use an object without them.
var identityFields = map[string]bool{"ratingKey": true, "key": true, "guid": true, "type": true}

// set parses a comma-separated list, nil for an empty one.
func set(csv string) map[string]bool {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	out := map[string]bool{}
	for _, v := range strings.Split(csv, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out[v] = true
		}
	}
	return out
}

// customizationOf reads the four parameters from the query and, for a
// match, from its JSON body, the query winning.
func customizationOf(r *http.Request, body map[string]any) customization {
	get := func(name string) string {
		if v := r.URL.Query().Get(name); v != "" {
			return v
		}
		if s, ok := body[name].(string); ok {
			return s
		}
		return ""
	}
	return customization{
		includeFields:   set(get("includeFields")),
		excludeFields:   set(get("excludeFields")),
		includeElements: set(get("includeElements")),
		excludeElements: set(get("excludeElements")),
	}
}

func (c customization) empty() bool {
	return c.includeFields == nil && c.excludeFields == nil && c.includeElements == nil && c.excludeElements == nil
}

// apply filters one Metadata object, as a JSON map, and the objects in its
// Children. An include list wins over an exclude list of the same kind.
func (c customization) apply(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		element := isElement(v)
		switch {
		case identityFields[k]:
		case element && c.includeElements != nil && !c.includeElements[k]:
			continue
		case element && c.includeElements == nil && c.excludeElements[k]:
			continue
		case !element && c.includeFields != nil && !c.includeFields[k]:
			continue
		case !element && c.includeFields == nil && c.excludeFields[k]:
			continue
		}
		if k == "Children" {
			v = c.applyChildren(v)
		}
		out[k] = v
	}
	return out
}

// applyChildren filters every object in a Children container, keeping the
// container's own size.
func (c customization) applyChildren(v any) any {
	children, ok := v.(map[string]any)
	if !ok {
		return v
	}
	items, ok := children["Metadata"].([]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(children))
	for k, cv := range children {
		out[k] = cv
	}
	filtered := make([]any, len(items))
	for i, it := range items {
		if obj, ok := it.(map[string]any); ok {
			filtered[i] = c.apply(obj)
		} else {
			filtered[i] = it
		}
	}
	out["Metadata"] = filtered
	return out
}

func isElement(v any) bool {
	switch v.(type) {
	case []any, map[string]any:
		return true
	}
	return false
}

// writeMetadata writes a metadata container, filtered by c when Plex asked
// for a customization.
func writeMetadata(w http.ResponseWriter, c customization, mc MetadataContainer) {
	if c.empty() {
		writeJSON(w, http.StatusOK, metadataContainerResponse{MediaContainer: mc})
		return
	}
	objs := make([]map[string]any, 0, len(mc.Metadata))
	for _, md := range mc.Metadata {
		b, err := json.Marshal(md)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "encode metadata")
			return
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			writeError(w, http.StatusInternalServerError, "encode metadata")
			return
		}
		objs = append(objs, c.apply(obj))
	}
	writeJSON(w, http.StatusOK, map[string]any{"MediaContainer": map[string]any{
		"offset": mc.Offset, "totalSize": mc.TotalSize, "identifier": mc.Identifier, "size": mc.Size, "Metadata": objs,
	}})
}
