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

package ui

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// libraryView is the library toolbar's state (design 2026-09-24, after
// Radarr's): ?sort with ?dir, the ?filter preset and, since 2026-11-10, the
// custom filter's conditions, a repeated ?f (projection.Condition). The
// page and its SSE stream parse it the same way and apply it through
// projection.Arrange, so a frame never shows rows the page would not.
type libraryView struct {
	Tab        projection.Tab
	Sort       projection.LibrarySort
	Desc       bool
	Filter     projection.LibraryFilter
	Conditions []projection.Condition
}

// parseLibraryView reads tab's view off a query; anything unknown is the
// default (title ascending, everything).
func parseLibraryView(tab projection.Tab, q url.Values) libraryView {
	v := libraryView{Tab: tab}
	v.Sort, _ = projection.ParseLibrarySort(tab, q.Get("sort"))
	v.Desc = q.Get("dir") == "desc"
	v.Filter, _ = projection.ParseLibraryFilter(tab, q.Get("filter"))
	v.Conditions = projection.ParseConditions(tab, q["f"])
	return v
}

// values are the view's parameters with the defaults left off, so a link
// to the default view is the plain page URL and two links to one view are
// the same string.
func (v libraryView) values() url.Values {
	out := url.Values{}
	if v.Sort != projection.SortTitle {
		out.Set("sort", string(v.Sort))
	}
	if v.Desc {
		out.Set("dir", "desc")
	}
	if v.Filter != projection.FilterAll {
		out.Set("filter", string(v.Filter))
	}
	for _, c := range v.Conditions {
		out.Add("f", c.String())
	}
	return out
}

// href is the view's page under base.
func (v libraryView) href(base string) string {
	if q := v.values().Encode(); q != "" {
		return base + "?" + q
	}
	return base
}

func (v libraryView) dir() string {
	if v.Desc {
		return "desc"
	}
	return "asc"
}

// query is the view as projection.Arrange reads it, at now.
func (v libraryView) query(now time.Time) projection.LibraryQuery {
	return projection.LibraryQuery{Tab: v.Tab, Sort: v.Sort, Desc: v.Desc, Filter: v.Filter, Conditions: v.Conditions, Now: now}
}

// arrange applies the view to a tab's items.
func (v libraryView) arrange(items []projection.LibraryItem) []projection.LibraryItem {
	return projection.Arrange(items, v.query(time.Now()))
}

// filtered reports whether the view keeps less than the whole tab.
func (v libraryView) filtered() bool {
	return v.Filter != projection.FilterAll || len(v.Conditions) > 0
}

// toolbar builds the Sort and Filter menus, the active filter's chips and
// the custom filter builder for a page at base over the tab's items: every
// entry links to its view, the current sort's entry to the same sort the
// other way round, as Radarr flips a sort by choosing it again. shown is
// how many items the view keeps.
func (v libraryView) toolbar(base string, items []projection.LibraryItem, shown int) views.LibraryToolbar {
	tb := views.LibraryToolbar{
		Tab: string(v.Tab), Sort: string(v.Sort), Dir: v.dir(), Filter: string(v.Filter),
		Query: v.values().Encode(), Shown: shown, Total: len(items), Filtered: v.filtered(),
	}
	for _, s := range projection.LibrarySorts(v.Tab) {
		w := v
		w.Sort, w.Desc = s, false
		c := views.Choice{Key: string(s), Label: s.Label(), Active: s == v.Sort}
		if c.Active {
			w.Desc = !v.Desc
			c.Dir = v.dir()
		}
		c.Href = w.href(base)
		tb.Sorts = append(tb.Sorts, c)
	}
	for _, f := range projection.LibraryFilters(v.Tab) {
		w := v
		w.Filter = f
		tb.Filters = append(tb.Filters, views.Choice{Key: string(f), Label: f.Label(), Href: w.href(base), Active: f == v.Filter && len(v.Conditions) == 0})
	}
	if v.Filter != projection.FilterAll {
		w := v
		w.Filter = projection.FilterAll
		tb.Chips = append(tb.Chips, views.FilterChip{Label: v.Filter.Label(), Href: w.href(base)})
	}
	for i, c := range v.Conditions {
		w := v
		// Not slices.Delete: TestUINeverWrites reads any .Delete as a write.
		w.Conditions = append(slices.Clone(v.Conditions[:i]), v.Conditions[i+1:]...)
		tb.Chips = append(tb.Chips, views.FilterChip{Label: conditionLabel(v.Tab, c), Href: w.href(base)})
	}
	if v.filtered() {
		w := v
		w.Filter, w.Conditions = projection.FilterAll, nil
		tb.ClearHref = w.href(base)
	}
	tb.Builder = v.builder(items)
	return tb
}

// builder is the custom filter dialog's state: the view's conditions as
// rows (one empty row when there are none), and what keeps the rest of the
// view when the dialog applies.
func (v libraryView) builder(items []projection.LibraryItem) views.FilterBuilder {
	b := views.FilterBuilder{Tab: string(v.Tab), Sort: string(v.Sort), Desc: v.Desc, Filter: string(v.Filter)}
	for i, c := range v.Conditions {
		b.Rows = append(b.Rows, filterRow(v.Tab, items, strconv.Itoa(i), c.Field, c.Op, c.Values))
	}
	if len(b.Rows) == 0 {
		b.Rows = append(b.Rows, filterRow(v.Tab, items, "0", "", "", nil))
	}
	return b
}

// filterFields are the builder's field choices for tab.
func filterFields(tab projection.Tab) []views.Choice {
	fields := projection.LibraryFields(tab)
	out := make([]views.Choice, 0, len(fields))
	for _, f := range fields {
		out = append(out, views.Choice{Key: f.Key, Label: f.Label})
	}
	return out
}

// filterRow is one builder row: field key (the tab's first field when
// unknown) with op (the field's first when it does not take op) and the
// values chosen, over the tab's items for an enum's or a list's choices.
func filterRow(tab projection.Tab, items []projection.LibraryItem, index, key string, op projection.Op, values []string) views.FilterRow {
	f, ok := projection.LookupField(tab, key)
	if !ok {
		f = projection.LibraryFields(tab)[0]
	}
	ops := projection.Ops(f.Type)
	if !slices.Contains(ops, op) {
		op = ops[0]
	}
	row := views.FilterRow{
		Index: index, Field: f.Key, Fields: filterFields(tab), Type: string(f.Type), Op: string(op), Unit: f.Unit,
	}
	for _, o := range ops {
		row.Ops = append(row.Ops, views.Choice{Key: string(o), Label: projection.OpLabel(f.Type, o), Active: o == op})
	}
	if len(values) > 0 {
		row.Value = values[0]
	}
	switch f.Type {
	case projection.FieldBool:
		if row.Value == "" {
			row.Value = "true"
		}
	case projection.FieldDate:
		row.Unit = ""
	case projection.FieldEnum, projection.FieldList:
		for _, val := range f.FieldValues(items) {
			row.Options = append(row.Options, views.FilterOption{
				Value: val, Label: fieldValueLabel(f, val), Checked: slices.Contains(values, val),
			})
		}
		// A value the URL names that no item carries any more stays
		// chosen, so applying the dialog again keeps it.
		for _, val := range values {
			if !slices.ContainsFunc(row.Options, func(o views.FilterOption) bool { return o.Value == val }) {
				row.Options = append(row.Options, views.FilterOption{Value: val, Label: fieldValueLabel(f, val), Checked: true})
			}
		}
	}
	return row
}

// fieldValueLabel shows one of a field's values: a language by name, a
// fixed value by its label.
func fieldValueLabel(f projection.Field, value string) string {
	if f.Language {
		return languageName(value)
	}
	return f.ValueLabel(value)
}

// conditionLabel is a condition's chip: "Genres has any of Anime, Drama".
func conditionLabel(tab projection.Tab, c projection.Condition) string {
	f, ok := projection.LookupField(tab, c.Field)
	if !ok {
		return c.String()
	}
	vals := make([]string, 0, len(c.Values))
	for _, v := range c.Values {
		switch f.Type {
		case projection.FieldBool:
			if v == "true" {
				v = "yes"
			} else {
				v = "no"
			}
		default:
			v = fieldValueLabel(f, v)
		}
		vals = append(vals, v)
	}
	label := f.Label + " " + projection.OpLabel(f.Type, c.Op) + " " + strings.Join(vals, ", ")
	switch {
	case f.Type == projection.FieldDate && c.Op != projection.OpBefore && c.Op != projection.OpAfter:
		label += " days"
	case f.Unit != "":
		label += " " + f.Unit
	}
	return label
}

// viewFromBuilder is the view a submitted builder asks for: base's sort,
// direction and preset (the dialog's hidden fields) and a condition per
// row that says something, rows named by the repeated "row" field and
// read from field.<row>, op.<row> and value.<row> (repeated for a list).
func viewFromBuilder(tab projection.Tab, form url.Values) libraryView {
	v := parseLibraryView(tab, form)
	v.Conditions = nil
	for _, row := range form["row"] {
		c, ok := projection.NewCondition(tab, form.Get("field."+row), projection.Op(form.Get("op."+row)), form["value."+row])
		if ok {
			v.Conditions = append(v.Conditions, c)
		}
	}
	return v
}
