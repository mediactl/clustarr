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

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// jsonString renders v as a JSON string literal without HTML-escaping --
// several embedded TRaSH regexes use .NET lookbehind/lookahead syntax
// (`(?<=`, `(?<!`) whose `<` encoding/json's default Marshal would mangle
// into `<`, silently breaking the pattern the loader compiles.
func jsonString(v string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		// v is always a plain Go string; Encode of a string cannot fail.
		panic(fmt.Sprintf("gen-catalogue: encode %q: %v", v, err))
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// renderCondition renders one condition compactly, on one line, with only
// the fields it actually carries -- in the same field order
// pkg/quality/catalogue/load.go's conditionJSON declares them (Kind, Name,
// Negate, Required, Pattern, Value, Resolution, ExceptLanguage, Flag), so
// this is literally "the same JSON shape the loader already consumes".
func renderCondition(c resolvedCondition) string {
	parts := []string{
		fmt.Sprintf("\"kind\": %s", jsonString(c.Kind)),
		fmt.Sprintf("\"name\": %s", jsonString(c.Name)),
	}
	if c.Negate {
		parts = append(parts, `"negate": true`)
	}
	if c.Required {
		parts = append(parts, `"required": true`)
	}
	if c.HasPattern {
		parts = append(parts, fmt.Sprintf("\"pattern\": %s", jsonString(c.Pattern)))
	}
	if c.HasValue {
		parts = append(parts, fmt.Sprintf("\"value\": %s", jsonString(c.Value)))
	}
	if c.HasResolution {
		parts = append(parts, fmt.Sprintf("\"resolution\": %d", c.Resolution))
	}
	if c.ExceptLanguage {
		parts = append(parts, `"exceptLanguage": true`)
	}
	if c.HasFlag {
		parts = append(parts, fmt.Sprintf("\"flag\": %s", jsonString(c.Flag)))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// renderTrashIDs renders a format's trashIds map compactly, apps in the
// order the manifest (and thus TestFormatDoesNotClaimAnUnrepresentedApp's
// documented curation) lists them.
func renderTrashIDs(apps []manifestApp) string {
	parts := make([]string, 0, len(apps))
	for _, a := range apps {
		parts = append(parts, fmt.Sprintf("%s: %s", jsonString(a.App), jsonString(a.TrashID)))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// renderScores renders a format's scores map compactly. Order is fixed
// (default, anime-radarr, anime-sonarr) rather than alphabetical --
// assembleScores explains why in its own doc comment.
func renderScores(scores []scoreEntry) string {
	parts := make([]string, 0, len(scores))
	for _, s := range scores {
		parts = append(parts, fmt.Sprintf("%s: %d", jsonString(s.Key), s.Value))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// renderFormat renders one format fully expanded: one field per line, one
// condition per line, matching formatJSON's own field order (Slug, Name,
// TrashIDs, Scores, Group, Conditions) -- Group is omitted entirely when
// empty (most formats carry no FormatGroup), never emitted as "".
func renderFormat(f resolvedFormat) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  {\n")
	fmt.Fprintf(&b, "    \"slug\": %s,\n", jsonString(f.Slug))
	fmt.Fprintf(&b, "    \"name\": %s,\n", jsonString(f.Name))
	fmt.Fprintf(&b, "    \"trashIds\": %s,\n", renderTrashIDs(f.TrashIDs))
	if f.Group != "" {
		fmt.Fprintf(&b, "    \"scores\": %s,\n", renderScores(f.Scores))
		fmt.Fprintf(&b, "    \"group\": %s,\n", jsonString(f.Group))
	} else {
		fmt.Fprintf(&b, "    \"scores\": %s,\n", renderScores(f.Scores))
	}
	if len(f.Conditions) == 0 {
		fmt.Fprintf(&b, "    \"conditions\": []\n")
	} else {
		fmt.Fprintf(&b, "    \"conditions\": [\n")
		for i, c := range f.Conditions {
			sep := ",\n"
			if i == len(f.Conditions)-1 {
				sep = "\n"
			}
			fmt.Fprintf(&b, "      %s%s", renderCondition(c), sep)
		}
		fmt.Fprintf(&b, "    ]\n")
	}
	fmt.Fprintf(&b, "  }")
	return b.String()
}

// renderFamily renders a whole data/formats/<family>.json document: a JSON
// array of formats in manifest order, 2-space indented, trailing newline.
func renderFamily(formats []resolvedFormat) []byte {
	var b strings.Builder
	b.WriteString("[\n")
	for i, f := range formats {
		b.WriteString(renderFormat(f))
		if i == len(formats)-1 {
			b.WriteString("\n")
		} else {
			b.WriteString(",\n")
		}
	}
	b.WriteString("]\n")
	return []byte(b.String())
}
