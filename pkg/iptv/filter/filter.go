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

// Package filter is xTeVe's channel filters (Live TV spec §4.2): which
// playlist entries reach the mapping table. The tuner and the ui use this
// one implementation, so the ui's counts are the tuner's.
package filter

import (
	"strings"

	"github.com/mediactl/clustarr/pkg/iptv/m3u"
)

// The filter types, as IPTVProvider spec.filters[].type spells them.
const (
	TypeGroupTitle = "groupTitle"
	TypeCustom     = "custom"
)

// Rule is one filter.
type Rule struct {
	Name          string
	Type          string
	Match         string
	Include       []string
	Exclude       []string
	CaseSensitive bool
	Enabled       bool
}

// Matches reports whether r selects e. A groupTitle rule matches e's group
// exactly; a custom rule matches a substring of e's name. Either way, the
// name must then contain one of Include (when set) and none of Exclude.
// Matching ignores case unless CaseSensitive. A disabled rule, or one of an
// unknown type, matches nothing.
func Matches(r Rule, e m3u.Entry) bool {
	if !r.Enabled {
		return false
	}
	fold := func(s string) string {
		if r.CaseSensitive {
			return s
		}
		return strings.ToLower(s)
	}
	name := fold(e.Name)
	switch r.Type {
	case TypeGroupTitle:
		if fold(e.Group) != fold(r.Match) {
			return false
		}
	case TypeCustom:
		if !strings.Contains(name, fold(r.Match)) {
			return false
		}
	default:
		return false
	}
	if len(r.Include) > 0 {
		found := false
		for _, w := range r.Include {
			if strings.Contains(name, fold(w)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, w := range r.Exclude {
		if strings.Contains(name, fold(w)) {
			return false
		}
	}
	return true
}

// Candidates is every entry some rule matches, once each, in playlist
// order. With no rule, nothing is a candidate (xTeVe's rule).
func Candidates(entries []m3u.Entry, rules []Rule) []m3u.Entry {
	var out []m3u.Entry
	for _, e := range entries {
		for _, r := range rules {
			if Matches(r, e) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// Preview counts the entries r alone matches: the ui's live count while a
// filter is edited.
func Preview(entries []m3u.Entry, r Rule) int {
	n := 0
	for _, e := range entries {
		if Matches(r, e) {
			n++
		}
	}
	return n
}
