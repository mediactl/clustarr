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

package filter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/iptv/filter"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
)

func entries() []m3u.Entry {
	return []m3u.Entry{
		{Key: "a", Name: "BBC One HD", Group: "UK | Entertainment"},
		{Key: "b", Name: "BBC Two SD", Group: "UK | Entertainment"},
		{Key: "c", Name: "Sky Sports Main Event", Group: "UK | Sports"},
		{Key: "d", Name: "sky news", Group: "UK | News"},
	}
}

func TestMatches(t *testing.T) {
	for _, tc := range []struct {
		name string
		rule filter.Rule
		want []string
	}{
		{"a group title matches its group exactly", filter.Rule{Type: "groupTitle", Match: "UK | Entertainment", Enabled: true}, []string{"a", "b"}},
		{"a group title is case-insensitive by default", filter.Rule{Type: "groupTitle", Match: "uk | entertainment", Enabled: true}, []string{"a", "b"}},
		{"a case-sensitive group title must match case", filter.Rule{Type: "groupTitle", Match: "uk | entertainment", CaseSensitive: true, Enabled: true}, nil},
		{"a group title is not a substring", filter.Rule{Type: "groupTitle", Match: "UK", Enabled: true}, nil},
		{"custom matches a substring of the name", filter.Rule{Type: "custom", Match: "sky", Enabled: true}, []string{"c", "d"}},
		{"a case-sensitive custom must match case", filter.Rule{Type: "custom", Match: "sky", CaseSensitive: true, Enabled: true}, []string{"d"}},
		{"include needs one of its words", filter.Rule{Type: "groupTitle", Match: "UK | Entertainment", Include: []string{"HD", "FHD"}, Enabled: true}, []string{"a"}},
		{"exclude drops any of its words", filter.Rule{Type: "custom", Match: "sky", Exclude: []string{"news"}, Enabled: true}, []string{"c"}},
		{"an unknown type matches nothing", filter.Rule{Type: "regex", Match: "sky", Enabled: true}, nil},
		{"a disabled rule matches nothing", filter.Rule{Type: "custom", Match: "sky"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, e := range entries() {
				if filter.Matches(tc.rule, e) {
					got = append(got, e.Key)
				}
			}
			assert.Equal(t, tc.want, got)
			assert.Equal(t, len(tc.want), filter.Preview(entries(), tc.rule))
		})
	}
}

func TestCandidatesAreTheUnionInPlaylistOrder(t *testing.T) {
	got := filter.Candidates(entries(), []filter.Rule{
		{Type: "custom", Match: "sky", Enabled: true},
		{Type: "groupTitle", Match: "UK | Sports", Enabled: true},
		{Type: "custom", Match: "BBC One", Enabled: true},
	})
	var keys []string
	for _, e := range got {
		keys = append(keys, e.Key)
	}
	assert.Equal(t, []string{"a", "c", "d"}, keys, "each entry once, in playlist order")
	assert.Empty(t, filter.Candidates(entries(), nil), "with no filter nothing is a candidate (xTeVe's rule)")
}
