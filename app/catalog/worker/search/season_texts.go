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

package search

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/release"
)

// maxSeasonNames caps the season's own names a season search asks a text
// indexer for beside the series title: each is one more query of that
// indexer, sent at its requestDelay.
const maxSeasonNames = 3

// seasonTexts are the further texts an anime season search asks an indexer
// that takes no id for (TargetIDs.SeasonTexts): the names the season itself
// is released under, then "<title> Snn" for a season past the first.
//
// An anime season's text is the series title alone, since its packs are
// named by absolute range or "Batch" (BuildSearchRequest), and a text
// indexer answers one page of that, newest first. For a franchise the page
// holds none of the season's packs: nyaa has 809 "Lupin III" torrents in its
// English-translated category and returned neither Part 6 batch, where the
// search of one of its episodes ("Lupin III 2", 455 results) happened to
// (talos, 2026-11-27; owner: "The sixth season should also show
// [SubsPlease] Lupin III - Part 6 (00-24) (1080p) [Batch]"). "Lupin III
// Part 6" finds both and "Lupin III S06" the one named so.
//
// The names are TheXEM's scene titles for the scene season (names), then
// the series' alternate titles that end in the season's number, in digits
// or as a Roman numeral ("Lupin III Part 6", "Lupin III Part II"): those
// that begin with the series title first, the shorter first. One equal to
// the title, or to one already taken, is left out. What comes back is
// judged by the search's identity as any other release is, so a name that
// reaches too far costs a query and nothing else.
func seasonTexts(title string, anime bool, stored, scene int32, names []decision.SceneSeasonName, alternates []catalogv1alpha1.AltTitle) []string {
	if !anime || title == "" || stored < 1 {
		return nil
	}
	seen := map[string]bool{release.CleanTitle(title): true}
	var out []string
	take := func(t string) {
		k := release.CleanTitle(t)
		if t == "" || seen[k] || len(out) >= maxSeasonNames {
			return
		}
		seen[k] = true
		out = append(out, t)
	}
	for _, n := range names {
		if n.Season == int(scene) {
			take(n.Title)
		}
	}
	var alts []string
	for _, a := range alternates {
		if a.SceneSeason != nil && *a.SceneSeason == scene || endsInNumber(a.Title, int(stored)) {
			alts = append(alts, a.Title)
		}
	}
	slices.SortStableFunc(alts, func(a, b string) int {
		ap, bp := strings.HasPrefix(a, title), strings.HasPrefix(b, title)
		switch {
		case ap != bp && ap:
			return -1
		case ap != bp:
			return 1
		}
		return len(a) - len(b)
	})
	for _, a := range alts {
		take(a)
	}
	if scene > 1 {
		out = append(out, fmt.Sprintf("%s S%02d", title, scene))
	}
	return out
}

// romanNumerals are the season numbers a title spells in Roman numerals.
var romanNumerals = []string{"", "I", "II", "III", "IV", "V", "VI", "VII", "VIII", "IX", "X", "XI", "XII", "XIII", "XIV", "XV", "XVI", "XVII", "XVIII", "XIX", "XX"}

// endsInNumber reports a title whose last word is n, in digits or as a
// Roman numeral, with more than that word to it.
func endsInNumber(title string, n int) bool {
	words := strings.Fields(title)
	if len(words) < 2 {
		return false
	}
	last := words[len(words)-1]
	if v, err := strconv.Atoi(last); err == nil {
		return v == n
	}
	return n > 0 && n < len(romanNumerals) && last == romanNumerals[n]
}

// seasonNamed are the further requests of an anime season search under the
// season's own names (TargetIDs.SeasonTexts): the same request with each as
// its text, for the indexers that take no id only (SearchRequest.TextOnly)
// -- one that takes the series' id already answered the season by it, and
// asking it again would spend its queries on the same releases. An
// index-only search and an episode's search make none.
func seasonNamed(req schema.SearchRequest, ids TargetIDs, task schema.SearchTask) []schema.SearchRequest {
	if task.IndexOnly || !req.Anime || req.Season == nil || req.Episode != nil {
		return nil
	}
	var out []schema.SearchRequest
	for _, t := range ids.SeasonTexts {
		r := req
		r.Text, r.TextOnly = t, true
		out = append(out, r)
	}
	return out
}
