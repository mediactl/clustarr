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

package plexfilter

import (
	"fmt"
	"strconv"
	"strings"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Describe is the Collections page's one-line reading of a collection: its
// filter in Plex Web's words ("Genre is Anime and Audio Language is en"),
// groups in parentheses and strings quoted, or a normal collection's size.
func Describe(spec catalogv1.PlexCollectionSpec) string {
	if spec.Members != nil {
		if n := len(spec.Members.Items); n != 1 {
			return fmt.Sprintf("%d items", n)
		}
		return "1 item"
	}
	if spec.Filter == nil {
		return ""
	}
	parts := make([]string, 0, len(spec.Filter.Rules))
	for _, r := range spec.Filter.Rules {
		if r.Field == "" && (r.Match != "" || len(r.Rules) > 0) {
			inner := make([]string, 0, len(r.Rules))
			for _, c := range r.Rules {
				inner = append(inner, describeCondition(spec.ItemType, c))
			}
			parts = append(parts, "("+strings.Join(inner, joiner(r.Match))+")")
			continue
		}
		parts = append(parts, describeCondition(spec.ItemType, catalogv1.PlexCondition{Field: r.Field, Operator: r.Operator, Value: r.Value}))
	}
	return strings.Join(parts, joiner(spec.Filter.Match))
}

func joiner(m catalogv1.PlexMatch) string {
	if m == catalogv1.PlexMatchAny {
		return " or "
	}
	return " and "
}

func describeCondition(t catalogv1.PlexItemType, c catalogv1.PlexCondition) string {
	title, ft := c.Field, FieldType("")
	if f, ok := Lookup(t, c.Field); ok {
		title, ft = f.Title, f.Type
	}
	s := title + " " + OperatorLabel(c.Operator)
	switch {
	case c.Operator == catalogv1.PlexOpIsTrue || c.Operator == catalogv1.PlexOpIsFalse:
		return s
	case ft == TypeString:
		return s + " " + strconv.Quote(c.Value)
	default:
		return s + " " + c.Value
	}
}
