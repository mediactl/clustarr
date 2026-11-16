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

package tuner

import (
	"regexp"
	"slices"

	"github.com/mediactl/clustarr/pkg/iptv/filter"
	"github.com/mediactl/clustarr/pkg/iptv/lineup"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// twins are a playlist's entries that carry a guide id, by the name the
// lineup's duplicate rule compares (lineup.NameKey): a provider lists a
// channel once per quality, the lineup keeps the best, and on argon-tv only
// a lower one may carry the guide id (owner, 2026-11-16: "We should take
// the guide id from the lower quality if none exists for higher quality").
// Only an entry the provider's filters admit is one: the same name in
// another group is another country's channel.
type twins struct {
	trim []*regexp.Regexp
	by   map[string][]m3u.Entry
}

// newTwins indexes entries; a trim pattern that does not compile leaves the
// names untrimmed (the lineup controller refuses such a spec).
func newTwins(entries []m3u.Entry, rules []filter.Rule, trim []string) *twins {
	tw := &twins{by: map[string][]m3u.Entry{}}
	tw.trim, _ = lineup.CompileTrim(trim)
	for _, e := range entries {
		if e.TvgID == "" || !admitted(e, rules) {
			continue
		}
		k := lineup.NameKey(e.Name, tw.trim)
		tw.by[k] = append(tw.by[k], e)
	}
	// The best quality first, then playlist order, as the duplicate rule
	// ranks them.
	for _, list := range tw.by {
		slices.SortStableFunc(list, func(a, b m3u.Entry) int {
			return lineup.Resolution(b.Name) - lineup.Resolution(a.Name)
		})
	}
	return tw
}

func admitted(e m3u.Entry, rules []filter.Rule) bool {
	for _, r := range rules {
		if filter.Matches(r, e) {
			return true
		}
	}
	return false
}

// of is the guide ids of e's twins, best first, e's own left out.
func (tw *twins) of(e m3u.Entry) []string {
	var out []string
	for _, o := range tw.by[lineup.NameKey(e.Name, tw.trim)] {
		if o.Key != e.Key && o.TvgID != e.TvgID && !slices.Contains(out, o.TvgID) {
			out = append(out, o.TvgID)
		}
	}
	return out
}

// autoMap finds an unmapped channel's guide channel: by its own entry's
// guide id or name (xmltv.AutoMap), else by the first of its twins' guide
// ids the guides hold.
func autoMap(ix *xmltv.Index, tvgID, name string, twinIDs []string) (xmltv.Mapping, bool) {
	if m, ok := ix.AutoMap(tvgID, name); ok {
		return m, true
	}
	for _, id := range twinIDs {
		if m, ok := ix.AutoMap(id, ""); ok {
			return m, true
		}
	}
	return xmltv.Mapping{}, false
}
