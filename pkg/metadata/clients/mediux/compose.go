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

package mediux

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Composed sets (owner, 2026-10-27: "find all image types by combining
// various sets for a series or movie"; the design's "Composed sets"):
// every image an item can take from MediUX is a slot, and each slot takes
// one set's image. The anchor -- the owner's pinned set, else the best set
// -- gives what it has; its creator's other sets come next, then the rest,
// so a look is mixed only where no one creator covers it.

// SlotKind is one kind of image an item takes from a set.
type SlotKind string

// Slot kinds.
const (
	SlotPoster       SlotKind = "poster"       // the item's own poster
	SlotBackdrop     SlotKind = "backdrop"     // the item's own background
	SlotSeasonPoster SlotKind = "seasonPoster" // a season's poster, specials (0) included
	SlotTitlecard    SlotKind = "titlecard"    // an episode's thumbnail
)

// Slot is one image of an item: Season names a season poster's season or a
// titlecard's, Episode a titlecard's episode.
type Slot struct {
	Kind    SlotKind
	Season  int32
	Episode int32
}

// SetKind is what a set was made for.
type SetKind string

// Set kinds.
const (
	SetShow       SetKind = "show"
	SetMovie      SetKind = "movie"
	SetCollection SetKind = "collection"
)

// SetRef names a set an image came from.
type SetRef struct {
	ID      string
	Title   string
	Creator string
	Kind    SetKind
	Updated time.Time
	// Popularity is MediUX's rank of the set among the item's sets of its
	// kind: 1 is the most popular (popularity_global grows with it); nil
	// when MediUX has not ranked it yet.
	Popularity *int
}

// SetUse is a set and how many of the item's images it supplied.
type SetUse struct {
	SetRef
	Images int
}

// Pick is one slot filled from one set.
type Pick struct {
	Slot Slot
	File File
	Set  string // the set's id
}

// Composition is an item's images from its sets.
type Composition struct {
	// Anchor is the set the look is built around.
	Anchor SetRef
	// Sets are the sets that supplied an image: the anchor first, then by
	// images supplied.
	Sets []SetUse
	// Picks are the images: the poster, the backdrop, the season posters
	// by season, the titlecards by season and episode.
	Picks  []Pick
	Reason string
}

// candidate is one set as composition sees it.
type candidate struct {
	ref   SetRef
	files map[Slot]File
	order int // input order, the last tie-break
}

func (c *candidate) has(s Slot) bool { _, ok := c.files[s]; return ok }

// seasonPosters is how many season posters c has: of the series' seasons
// when the policy knows them, else all of its own.
func (c *candidate) seasonPosters(p Policy) int {
	n := 0
	if len(p.Seasons) > 0 {
		for _, s := range p.Seasons {
			if c.has(Slot{Kind: SlotSeasonPoster, Season: s}) {
				n++
			}
		}
		return n
	}
	for s := range c.files {
		if s.Kind == SlotSeasonPoster {
			n++
		}
	}
	return n
}

// cards is how many titlecards c has for season (every season when
// season is nil): at most the series' episodes when the policy knows them.
func (c *candidate) cards(p Policy, season *int32) int {
	by := map[int32]int{}
	for s := range c.files {
		if s.Kind == SlotTitlecard && (season == nil || s.Season == *season) {
			by[s.Season]++
		}
	}
	n := 0
	for s, k := range by {
		if want, ok := p.Episodes[s]; ok && len(p.Episodes) > 0 {
			k = min(k, int(want))
		} else if len(p.Episodes) > 0 {
			k = 0 // a season the series does not have
		}
		n += k
	}
	return n
}

// firstCardLanguage is the language of c's first titlecard of season.
func (c *candidate) firstCardLanguage(season int32) string {
	best := Slot{Episode: 1 << 30}
	for s := range c.files {
		if s.Kind == SlotTitlecard && s.Season == season && s.Episode < best.Episode {
			best = s
		}
	}
	return c.files[best].Language
}

func showCandidate(s *ShowSet, order int) *candidate {
	c := &candidate{ref: SetRef{ID: s.ID, Title: s.Title, Creator: s.Creator.Username, Kind: SetShow, Updated: s.Updated, Popularity: s.Popularity}, files: map[Slot]File{}, order: order}
	if len(s.Poster) > 0 {
		c.files[Slot{Kind: SlotPoster}] = s.Poster[0]
	}
	if len(s.Backdrop) > 0 {
		c.files[Slot{Kind: SlotBackdrop}] = s.Backdrop[0]
	}
	for _, sp := range s.SeasonPosters {
		k := Slot{Kind: SlotSeasonPoster, Season: sp.Season}
		if !c.has(k) && sp.Season >= 0 {
			c.files[k] = sp.File
		}
	}
	for _, t := range s.Titlecards {
		k := Slot{Kind: SlotTitlecard, Season: t.Season, Episode: t.Episode}
		if !c.has(k) && t.Episode >= 1 && t.Season >= 0 {
			c.files[k] = t.File
		}
	}
	return c
}

func movieCandidate(s *MovieSet, kind SetKind, order int) *candidate {
	c := &candidate{ref: SetRef{ID: s.ID, Title: s.Title, Creator: s.Creator.Username, Kind: kind, Updated: s.Updated, Popularity: s.Popularity}, files: map[Slot]File{}, order: order}
	if len(s.Poster) > 0 {
		c.files[Slot{Kind: SlotPoster}] = s.Poster[0]
	}
	if len(s.Backdrop) > 0 {
		c.files[Slot{Kind: SlotBackdrop}] = s.Backdrop[0]
	}
	return c
}

// ComposeShow composes a series' images from its sets; pinned, when it
// names one of them, anchors. Nil when no set has an image.
func ComposeShow(sets []ShowSet, p Policy, pinned string) *Composition {
	cands := make([]*candidate, 0, len(sets))
	for i := range sets {
		cands = append(cands, showCandidate(&sets[i], i))
	}
	return compose(cands, p, pinned)
}

// ComposeMovie composes a film's images from its own sets and the
// collection sets' images of it; pinned, when it names one, anchors. Nil
// when no set has an image.
func ComposeMovie(m *Movie, p Policy, pinned string) *Composition {
	if m == nil {
		return nil
	}
	cands := make([]*candidate, 0, len(m.Sets)+len(m.CollectionSets))
	for i := range m.Sets {
		cands = append(cands, movieCandidate(&m.Sets[i], SetMovie, len(cands)))
	}
	for i := range m.CollectionSets {
		cands = append(cands, movieCandidate(&m.CollectionSets[i], SetCollection, len(cands)))
	}
	return compose(cands, p, pinned)
}

// key is a ranking, compared term by term, larger first.
type key [10]int

func better(a, b key) bool { return slices.Compare(a[:], b[:]) > 0 }

// best is the candidate of cands with the largest k, nil for none.
func best(cands []*candidate, k func(*candidate) key) *candidate {
	var out *candidate
	var top key
	for _, c := range cands {
		if v := k(c); out == nil || better(v, top) {
			out, top = c, v
		}
	}
	return out
}

func compose(all []*candidate, p Policy, pinned string) *Composition {
	cands := slices.DeleteFunc(slices.Clone(all), func(c *candidate) bool { return len(c.files) == 0 })
	if len(cands) == 0 {
		return nil
	}
	poster, backdrop := Slot{Kind: SlotPoster}, Slot{Kind: SlotBackdrop}
	anchor := (*candidate)(nil)
	if pinned != "" {
		anchor = best(cands, func(c *candidate) key {
			if c.ref.ID == pinned {
				return key{1}
			}
			return key{}
		})
		if anchor.ref.ID != pinned {
			anchor = nil
		}
	}
	if anchor == nil {
		anchor = best(cands, func(c *candidate) key {
			f := c.files[poster]
			return key{bit(c.has(poster)), p.creatorRank(c), p.languageScore(f.Language), c.seasonPosters(p), c.cards(p, nil), bit(c.has(backdrop)), kindScore(c), popScore(c), updated(c), -c.order}
		})
	}

	// The poster group follows the set that supplies the poster: the
	// anchor's own, else the one the anchor's ranking picks.
	group := func(c *candidate) int { return bit(c.has(poster)) + c.seasonPosters(p) + bit(c.has(backdrop)) }
	posterKey := func(lead *candidate, s Slot) func(*candidate) key {
		return func(c *candidate) key {
			return key{bit(c == lead), bit(sameCreator(c, lead)), p.creatorRank(c), p.languageScore(c.files[s].Language), group(c), kindScore(c), popScore(c), updated(c), -c.order}
		}
	}
	posterLead := anchor
	if !anchor.has(poster) {
		if c := best(having(cands, poster), posterKey(anchor, poster)); c != nil {
			posterLead = c
		}
	}

	// Titlecards are chosen per season: the season's lead is the anchor
	// creator's (else a preferred creator's) set with the most cards for
	// it, and its gaps fill from the rest.
	seasonLead := map[int32]*candidate{}
	leadOf := func(season int32) *candidate {
		if l, ok := seasonLead[season]; ok {
			return l
		}
		var with []*candidate
		for _, c := range cands {
			if c.cards(Policy{}, &season) > 0 {
				with = append(with, c)
			}
		}
		l := best(with, func(c *candidate) key {
			return key{bit(sameCreator(c, anchor)), p.creatorRank(c), c.cards(Policy{}, &season), p.languageScore(c.firstCardLanguage(season)), kindScore(c), popScore(c), updated(c), -c.order}
		})
		seasonLead[season] = l
		return l
	}

	slots := map[Slot]bool{}
	for _, c := range cands {
		for s := range c.files {
			slots[s] = true
		}
	}
	ordered := make([]Slot, 0, len(slots))
	for s := range slots {
		ordered = append(ordered, s)
	}
	slices.SortFunc(ordered, compareSlots)

	out := &Composition{}
	used := map[*candidate]int{}
	for _, s := range ordered {
		var k func(*candidate) key
		switch s.Kind {
		case SlotTitlecard:
			lead := leadOf(s.Season)
			season := s.Season
			k = func(c *candidate) key {
				return key{bit(c == lead), bit(sameCreator(c, lead)), p.creatorRank(c), p.languageScore(c.files[s].Language), c.cards(Policy{}, &season), kindScore(c), popScore(c), updated(c), -c.order}
			}
		default:
			k = posterKey(posterLead, s)
		}
		c := best(having(cands, s), k)
		out.Picks = append(out.Picks, Pick{Slot: s, File: c.files[s], Set: c.ref.ID})
		used[c]++
	}

	// A pinned or chosen anchor that every slot went past is no anchor:
	// the set that supplied the most is.
	if used[anchor] == 0 {
		anchor = best(cands, func(c *candidate) key { return key{used[c], -c.order} })
	}
	out.Anchor = anchor.ref
	out.Sets = append(out.Sets, SetUse{SetRef: anchor.ref, Images: used[anchor]})
	var rest []*candidate
	for c := range used {
		if c != anchor {
			rest = append(rest, c)
		}
	}
	slices.SortFunc(rest, func(a, b *candidate) int {
		return cmp.Or(cmp.Compare(used[b], used[a]), cmp.Compare(a.order, b.order))
	})
	filled := 0
	for _, c := range rest {
		out.Sets = append(out.Sets, SetUse{SetRef: c.ref, Images: used[c]})
		filled += used[c]
	}
	out.Reason = p.why(anchor, pinned)
	if len(rest) > 0 {
		out.Reason += fmt.Sprintf("; %d more sets fill %d images", len(rest), filled)
	}
	return out
}

// why is the anchor's case, for the log.
func (p Policy) why(c *candidate, pinned string) string {
	var why []string
	if pinned != "" && c.ref.ID == pinned {
		why = append(why, "pinned")
	}
	why = append(why, "anchor "+c.ref.ID+" by "+c.ref.Creator)
	if p.creatorRank(c) > 0 {
		why = append(why, "preferred creator "+c.ref.Creator)
	}
	if f, ok := c.files[Slot{Kind: SlotPoster}]; ok && f.Language != "" && p.languageScore(f.Language) == 2 {
		why = append(why, "poster in "+f.Language)
	}
	seasons := fmt.Sprintf("%d season posters", c.seasonPosters(p))
	if len(p.Seasons) > 0 {
		seasons = fmt.Sprintf("%d of %d season posters", c.seasonPosters(p), len(p.Seasons))
	}
	rank := "unranked"
	if c.ref.Popularity != nil {
		rank = fmt.Sprintf("popularity %d", *c.ref.Popularity)
	}
	why = append(why, fmt.Sprintf("%s, %d titlecards, %s", seasons, c.cards(p, nil), rank))
	return strings.Join(why, "; ")
}

// creatorRank is how early c's creator is among the preferred, 0 when not.
func (p Policy) creatorRank(c *candidate) int {
	if i := slices.IndexFunc(p.PreferredCreators, func(u string) bool { return strings.EqualFold(u, c.ref.Creator) }); i >= 0 {
		return len(p.PreferredCreators) - i
	}
	return 0
}

// languageScore ranks an image's language: one the policy wants (2), then
// none named or no preference (1), then another (0).
func (p Policy) languageScore(lang string) int {
	switch {
	case len(p.Languages) == 0 || lang == "":
		return 1
	case slices.ContainsFunc(p.Languages, func(l string) bool { return strings.EqualFold(l, lang) }):
		return 2
	default:
		return 0
	}
}

func having(cands []*candidate, s Slot) []*candidate {
	var out []*candidate
	for _, c := range cands {
		if c.has(s) {
			out = append(out, c)
		}
	}
	return out
}

func sameCreator(a, b *candidate) bool {
	return a != nil && b != nil && a.ref.Creator != "" && strings.EqualFold(a.ref.Creator, b.ref.Creator)
}

// kindScore puts a set made for the item over a collection's.
func kindScore(c *candidate) int { return bit(c.ref.Kind != SetCollection) }

// popScore turns MediUX's rank into a score: rank 1 highest, unranked
// lowest.
func popScore(c *candidate) int {
	if c.ref.Popularity == nil || *c.ref.Popularity < 1 {
		return 0
	}
	return max(1, 1<<20-*c.ref.Popularity)
}

func updated(c *candidate) int { return int(c.ref.Updated.Unix() / 60) }

func bit(v bool) int {
	if v {
		return 1
	}
	return 0
}

// compareSlots orders the poster, the backdrop, the season posters by
// season, then the titlecards by season and episode.
func compareSlots(a, b Slot) int {
	rank := map[SlotKind]int{SlotPoster: 0, SlotBackdrop: 1, SlotSeasonPoster: 2, SlotTitlecard: 3}
	return cmp.Or(cmp.Compare(rank[a.Kind], rank[b.Kind]), cmp.Compare(a.Season, b.Season), cmp.Compare(a.Episode, b.Episode))
}
