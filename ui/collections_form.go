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
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/plexfilter"
)

// The Collections editor's form
// (docs/superpowers/specs/2026-11-10-plex-collections-design.md): general
// fields by name -- library, title, itemType, kind (smart | normal),
// summary, sortTitle -- a smart collection's match, sort.field,
// sort.descending and limit, its top-level rules as rule.<i>.field |
// operator | value | match, and a group's conditions as
// rule.<i>.<j>.field | operator | value; a normal collection's members as
// member.<i>.kind | name; and the presentation: poster.url, background.url,
// settings.mode, settings.order, visibility.managed and
// visibility.libraryRecommended | home | friendsHome. Indexes are dense
// from 0 as the editor renders them; parsing orders them by number all the
// same.

var errEditorOp = errors.New("editor: no such rule")

// parseCollectionForm reads the editor's form into a spec. An empty
// summary or sort title leaves the field out (Plex keeps its own).
func parseCollectionForm(v url.Values) (catalogv1.PlexCollectionSpec, error) {
	spec := catalogv1.PlexCollectionSpec{
		Library:  strings.TrimSpace(v.Get("library")),
		Title:    strings.TrimSpace(v.Get("title")),
		ItemType: catalogv1.PlexItemType(v.Get("itemType")),
	}
	if spec.ItemType == "" {
		spec.ItemType = catalogv1.PlexItemMovie
	}
	if s := strings.TrimSpace(v.Get("summary")); s != "" {
		spec.Summary = &s
	}
	if s := strings.TrimSpace(v.Get("sortTitle")); s != "" {
		spec.SortTitle = &s
	}
	if u := strings.TrimSpace(v.Get("poster.url")); u != "" {
		spec.Poster = &catalogv1.PlexImage{URL: u}
	}
	if u := strings.TrimSpace(v.Get("background.url")); u != "" {
		spec.Background = &catalogv1.PlexImage{URL: u}
	}
	mode, order := catalogv1.PlexCollectionMode(v.Get("settings.mode")), catalogv1.PlexCollectionOrder(v.Get("settings.order"))
	// "keep" is the selects' "Keep Plex's".
	if mode == "keep" {
		mode = ""
	}
	if order == "keep" || v.Get("kind") != "normal" {
		order = ""
	}
	if mode != "" || order != "" {
		spec.Settings = &catalogv1.PlexCollectionSettings{Mode: mode, Order: order}
	}
	if formBool(v.Get("visibility.managed")) {
		spec.Visibility = &catalogv1.PlexVisibility{
			LibraryRecommended: formBool(v.Get("visibility.libraryRecommended")),
			Home:               formBool(v.Get("visibility.home")),
			FriendsHome:        formBool(v.Get("visibility.friendsHome")),
		}
	}
	if v.Get("kind") == "normal" {
		spec.Members = &catalogv1.PlexMembers{}
		for _, i := range formIndexes(v, "member.") {
			p := fmt.Sprintf("member.%d.", i)
			if name := strings.TrimSpace(v.Get(p + "name")); name != "" {
				spec.Members.Items = append(spec.Members.Items, catalogv1.PlexMember{Kind: v.Get(p + "kind"), Name: name})
			}
		}
		return spec, nil
	}

	f := &catalogv1.PlexFilter{Match: catalogv1.PlexMatch(v.Get("match"))}
	if f.Match == "" {
		f.Match = catalogv1.PlexMatchAll
	}
	for _, i := range formIndexes(v, "rule.") {
		p := fmt.Sprintf("rule.%d.", i)
		if m := v.Get(p + "match"); m != "" {
			g := catalogv1.PlexFilterRule{Match: catalogv1.PlexMatch(m)}
			for _, j := range formIndexes(v, p) {
				q := fmt.Sprintf("%s%d.", p, j)
				g.Rules = append(g.Rules, catalogv1.PlexCondition{
					Field: v.Get(q + "field"), Operator: catalogv1.PlexOperator(v.Get(q + "operator")), Value: strings.TrimSpace(v.Get(q + "value")),
				})
			}
			f.Rules = append(f.Rules, g)
			continue
		}
		f.Rules = append(f.Rules, catalogv1.PlexFilterRule{
			Field: v.Get(p + "field"), Operator: catalogv1.PlexOperator(v.Get(p + "operator")), Value: strings.TrimSpace(v.Get(p + "value")),
		})
	}
	spec.Filter = f
	if s := strings.TrimSpace(v.Get("sort.field")); s != "" {
		spec.Sort = &catalogv1.PlexSort{Field: s, Descending: formBool(v.Get("sort.descending"))}
	}
	if s := strings.TrimSpace(v.Get("limit")); s != "" {
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil || n < 1 {
			return spec, fmt.Errorf("limit: %q is not a whole number of at least 1", s)
		}
		l := int32(n)
		spec.Limit = &l
	}
	return spec, nil
}

// formIndexes are the distinct numbers k in keys prefix+"<k>.", in order.
func formIndexes(v url.Values, prefix string) []int {
	seen := map[int]bool{}
	for key := range v {
		rest, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		num, _, ok := strings.Cut(rest, ".")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(num); err == nil && n >= 0 {
			seen[n] = true
		}
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func formBool(s string) bool { return s == "on" || s == "true" || s == "1" }

// blankCondition is a new row: the item type's first field and the first
// operator its type takes.
func blankCondition(t catalogv1.PlexItemType) catalogv1.PlexCondition {
	fields := plexfilter.Fields(t)
	if len(fields) == 0 {
		return catalogv1.PlexCondition{}
	}
	return catalogv1.PlexCondition{Field: fields[0].Key, Operator: plexfilter.Operators(fields[0].Type)[0]}
}

// editorOp changes the spec as one of the editor's buttons asks: add-rule
// (at "" for the top level, "<i>" for group i), add-group, remove (at "<i>"
// or "<i>.<j>"), add-member (at "<Kind>/<name>", once), remove-member (at
// "<i>"), clear-image (at poster or background), or refresh, which only
// settles each condition's operator on one its field takes after a field
// changed.
func editorOp(spec *catalogv1.PlexCollectionSpec, op, at string) error {
	switch op {
	case "add-member", "remove-member":
		return memberOp(spec, op, at)
	case "clear-image":
		switch at {
		case "poster":
			spec.Poster = nil
		case "background":
			spec.Background = nil
		default:
			return fmt.Errorf("%w: no image %q", errEditorOp, at)
		}
		return nil
	}
	if spec.Filter == nil && op != "refresh" {
		return fmt.Errorf("%w: a normal collection has no filter", errEditorOp)
	}
	switch op {
	case "add-rule":
		c := blankCondition(spec.ItemType)
		if at == "" {
			spec.Filter.Rules = append(spec.Filter.Rules, catalogv1.PlexFilterRule{Field: c.Field, Operator: c.Operator})
			return nil
		}
		i, err := ruleIndex(spec, at)
		if err != nil {
			return err
		}
		if spec.Filter.Rules[i].Match == "" {
			return fmt.Errorf("%w: rule %d is no group", errEditorOp, i)
		}
		spec.Filter.Rules[i].Rules = append(spec.Filter.Rules[i].Rules, c)
	case "add-group":
		spec.Filter.Rules = append(spec.Filter.Rules, catalogv1.PlexFilterRule{
			Match: catalogv1.PlexMatchAll, Rules: []catalogv1.PlexCondition{blankCondition(spec.ItemType)},
		})
	case "remove":
		outer, inner, nested := strings.Cut(at, ".")
		i, err := ruleIndex(spec, outer)
		if err != nil {
			return err
		}
		if !nested {
			spec.Filter.Rules = removeAt(spec.Filter.Rules, i)
			return nil
		}
		j, err := strconv.Atoi(inner)
		if err != nil || j < 0 || j >= len(spec.Filter.Rules[i].Rules) {
			return fmt.Errorf("%w: %q", errEditorOp, at)
		}
		g := &spec.Filter.Rules[i]
		g.Rules = removeAt(g.Rules, j)
		if len(g.Rules) == 0 {
			spec.Filter.Rules = removeAt(spec.Filter.Rules, i)
		}
	case "refresh":
	default:
		return fmt.Errorf("%w: op %q", errEditorOp, op)
	}
	settleOperators(spec)
	return nil
}

// removeAt is s without its element i. (Not slices.Delete: TestUINeverWrites
// refuses any call named Delete outside ui/actions.)
func removeAt[T any](s []T, i int) []T {
	return append(s[:i:i], s[i+1:]...)
}

func ruleIndex(spec *catalogv1.PlexCollectionSpec, at string) (int, error) {
	i, err := strconv.Atoi(at)
	if err != nil || i < 0 || i >= len(spec.Filter.Rules) {
		return 0, fmt.Errorf("%w: %q", errEditorOp, at)
	}
	return i, nil
}

// settleOperators gives every condition whose field is unknown to the item
// type the type's first field, and every operator its field's type does
// not take the type's first.
func settleOperators(spec *catalogv1.PlexCollectionSpec) {
	if spec.Filter == nil {
		return
	}
	fix := func(field *string, op *catalogv1.PlexOperator) {
		f, ok := plexfilter.Lookup(spec.ItemType, *field)
		if !ok {
			b := blankCondition(spec.ItemType)
			*field, *op = b.Field, b.Operator
			return
		}
		if !slices.Contains(plexfilter.Operators(f.Type), *op) {
			*op = plexfilter.Operators(f.Type)[0]
		}
	}
	for i := range spec.Filter.Rules {
		r := &spec.Filter.Rules[i]
		if r.Match != "" {
			for j := range r.Rules {
				fix(&r.Rules[j].Field, &r.Rules[j].Operator)
			}
			continue
		}
		fix(&r.Field, &r.Operator)
	}
}

func memberOp(spec *catalogv1.PlexCollectionSpec, op, at string) error {
	if spec.Members == nil {
		return fmt.Errorf("%w: a smart collection has no members", errEditorOp)
	}
	if op == "remove-member" {
		i, err := strconv.Atoi(at)
		if err != nil || i < 0 || i >= len(spec.Members.Items) {
			return fmt.Errorf("%w: member %q", errEditorOp, at)
		}
		spec.Members.Items = removeAt(spec.Members.Items, i)
		return nil
	}
	kind, name, ok := strings.Cut(at, "/")
	if !ok || name == "" || (kind != "Movie" && kind != "Series") {
		return fmt.Errorf("%w: member %q", errEditorOp, at)
	}
	m := catalogv1.PlexMember{Kind: kind, Name: name}
	if !slices.Contains(spec.Members.Items, m) {
		spec.Members.Items = append(spec.Members.Items, m)
	}
	return nil
}

// formGeneration is the generation the editor read, posted back with the
// form; 0 when absent or not a number.
func formGeneration(v url.Values) int64 {
	n, err := strconv.ParseInt(v.Get("generation"), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
