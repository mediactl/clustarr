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

package views

import (
	"context"
	"encoding/json"
	"io"
	"maps"

	"github.com/a-h/templ"
)

// SelectionVerb is one thing the selection bar does to the selected rows.
type SelectionVerb struct {
	// Value is what the bar posts in its verb field.
	Value string
	Label string
	Icon  string
	// Destructive tints the verb; the server asks before it acts.
	Destructive bool
}

// SelectionBar is a page's selection bar.
type SelectionBar struct {
	// Action is the bulk route the verbs post to.
	Action string
	// Field is the verb's field name; "" is "action".
	Field string
	Verbs []SelectionVerb
	// Extra are dialog triggers for verbs that need a value first.
	Extra templ.Component
	Attrs templ.Attributes
}

// selectionBarClass is the bar's look, the library's bulk bar's too: a
// content-width glass bar centred at the page's foot.
const selectionBarClass = "glass sticky z-20 mx-auto mt-6 flex w-fit max-w-full items-center gap-2 rounded-xl border border-border bg-background/55 p-1.5 pl-3 shadow-lg shadow-black/30"

func verbClass(v SelectionVerb) string {
	if v.Destructive {
		return "text-destructive"
	}
	return ""
}

// verbAttrs post the verb through htmx into #action-status; rowselect.js
// adds the selected rows as "item" fields at htmx:configRequest and stops
// the request when nothing is selected.
func (b SelectionBar) verbAttrs(v SelectionVerb) templ.Attributes {
	field := b.Field
	if field == "" {
		field = "action"
	}
	vals, _ := json.Marshal(map[string]string{field: v.Value})
	return templ.Attributes{
		"hx-post":          b.Action,
		"hx-vals":          string(vals),
		"hx-target":        "#action-status",
		"hx-swap":          "innerHTML",
		"hx-disabled-elt":  "this",
		"data-rows-action": v.Value,
		"aria-label":       v.Label,
		"title":            v.Label,
	}
}

// RowVerbs are the verbs every library child row takes: an Episode's, an
// Album's, a Book's and an Issue's one editable field is monitored, and
// each can be searched.
func RowVerbs() []SelectionVerb {
	return []SelectionVerb{
		{Value: "monitor", Label: "Monitor", Icon: "bookmark-check"},
		{Value: "unmonitor", Label: "Unmonitor", Icon: "bookmark-x"},
		{Value: "search", Label: "Search", Icon: "search"},
	}
}

type oobRowKey struct{}

// RowAttrs marks a selectable row: data-row-ref (namespace/kind/name)
// beside the row's own attributes. Rendered through OOBRow the row also
// carries the out-of-band swap that replaces the row of that ref wherever
// it is in the page.
func RowAttrs(ctx context.Context, ref string, attrs templ.Attributes) templ.Attributes {
	out := templ.Attributes{"data-row-ref": ref}
	maps.Copy(out, attrs)
	if oob, _ := ctx.Value(oobRowKey{}).(bool); oob {
		out["hx-swap-oob"] = "outerHTML:[data-row-ref='" + ref + "']"
	}
	return out
}

// OOBRow is row as an out-of-band swap: inside <template>, since a bare
// <tr> is dropped by the HTML parser outside a table (htmx reads
// out-of-band elements out of templates), and rendered with the flag
// RowAttrs reads.
func OOBRow(row templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if _, err := io.WriteString(w, "<template>"); err != nil {
			return err
		}
		if err := row.Render(context.WithValue(ctx, oobRowKey{}, true), w); err != nil {
			return err
		}
		_, err := io.WriteString(w, "</template>")
		return err
	})
}
