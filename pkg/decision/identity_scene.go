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

package decision

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/mediactl/clustarr/pkg/release"
)

// EpisodeNumbering is one episode's place in one numbering scheme.
// Absolute is 0 when the scheme gives the episode none.
type EpisodeNumbering struct {
	Season   int
	Episode  int
	Absolute int
}

// SceneMapping is one row of a series' scene-numbering table: the numbering
// scene releases use for an episode, and the TVDB numbering the catalog uses
// for the same episode. It is TheXEM's per-episode mapping row (Sonarr's
// XemProxy; Episode.SceneSeasonNumber/SceneEpisodeNumber/
// SceneAbsoluteEpisodeNumber once Sonarr has applied it), as plain data so
// this package does not depend on whichever client fetched it.
type SceneMapping struct {
	Scene EpisodeNumbering
	TVDB  EpisodeNumbering
	// Title is the TVDB episode's own title, "" when the caller does not
	// hold it. A release that carries the title of the episode its number
	// names in TVDB's numbering is numbered that way, whatever the scene
	// row for that number says (releaseCoverage).
	Title string
}

// SceneSeasonName is a title scene releases use for one season of a series
// (TheXEM's names table, Sonarr's XemService scene mappings): a release
// under it numbers its own seasons from that one, so "New Panty & Stocking
// with Garterbelt S01E07" is scene season 2's episode 7.
type SceneSeasonName struct {
	Title  string
	Season int
}

type seasonEpisode struct{ season, episode int }

// sceneIndex is Identity.SceneMappings keyed for lookup, built once per
// Evaluate. The zero value maps nothing.
type sceneIndex struct {
	byEpisode  map[seasonEpisode][]EpisodeNumbering
	bySeason   map[int][]EpisodeNumbering
	byAbsolute map[int][]EpisodeNumbering
	// titles is each TVDB episode's title key, where the rows carry one;
	// seasonNames the scene season each of the series' scene titles names.
	titles      map[seasonEpisode]string
	seasonNames map[string]int
}

// newIdentitySceneIndex is an Identity's scene table and scene season
// names, keyed for lookup.
func newIdentitySceneIndex(id Identity) sceneIndex {
	idx := newSceneIndex(id.SceneMappings)
	for _, n := range id.SceneSeasonNames {
		k := titleKey(n.Title)
		if k == "" || n.Season < 1 {
			continue
		}
		if idx.seasonNames == nil {
			idx.seasonNames = map[string]int{}
		}
		idx.seasonNames[k] = n.Season
	}
	return idx
}

// sceneSeason is the scene season a release's season number means under
// its title: a title that is one of the series' scene names for a later
// season numbers its own first season from there. Any other season number
// under such a title, and any title that is not one, is read as written.
func (idx sceneIndex) sceneSeason(title string, season int) int {
	if n, ok := idx.seasonNames[titleKey(title)]; ok && n > 1 && season == 1 {
		return n
	}
	return season
}

// titled reports whether title is the title of TVDB's episode e of season:
// the sign of a release numbered by TVDB on a series scene numbers
// otherwise.
func (idx sceneIndex) titled(season, e int, title string) bool {
	if title == "" {
		return false
	}
	want := idx.titles[seasonEpisode{season, e}]
	return want != "" && want == titleKey(title)
}

func newSceneIndex(rows []SceneMapping) sceneIndex {
	if len(rows) == 0 {
		return sceneIndex{}
	}
	idx := sceneIndex{
		byEpisode:  map[seasonEpisode][]EpisodeNumbering{},
		bySeason:   map[int][]EpisodeNumbering{},
		byAbsolute: map[int][]EpisodeNumbering{},
		titles:     map[seasonEpisode]string{},
	}
	for _, r := range rows {
		if r.TVDB.Episode <= 0 {
			// A row that names no TVDB episode maps a scene number onto
			// nothing; reading it as a mapping would make that scene number
			// unmatchable instead of falling back to its literal reading.
			continue
		}
		if k := titleKey(r.Title); r.Title != "" && k != "" {
			idx.titles[seasonEpisode{r.TVDB.Season, r.TVDB.Episode}] = k
		}
		if r.Scene.Episode > 0 {
			k := seasonEpisode{r.Scene.Season, r.Scene.Episode}
			idx.byEpisode[k] = append(idx.byEpisode[k], r.TVDB)
			idx.bySeason[r.Scene.Season] = append(idx.bySeason[r.Scene.Season], r.TVDB)
		}
		if r.Scene.Absolute > 0 {
			idx.byAbsolute[r.Scene.Absolute] = append(idx.byAbsolute[r.Scene.Absolute], r.TVDB)
		}
	}
	return idx
}

// coverage is what a release's numbering covers in TVDB terms, which is what
// Identity.Season/Episodes/Absolute are in.
type coverage struct {
	pairs       map[seasonEpisode]bool
	fullSeasons map[int]bool // a pack of the season, read literally
	absolutes   map[int]bool
	mapped      []EpisodeNumbering // every TVDB episode a scene lookup produced, for messages
}

// releaseCoverage reads a release's numbering through the series' scene
// mapping first and literally second, exactly as Sonarr's ParsingService does
// for a release from an indexer (sceneSource) on a series that uses scene
// numbering (Sonarr src/NzbDrone.Core/Parser/ParsingService.cs, develop):
//
//   - A full-season pack is the TVDB episodes of that SCENE season
//     (GetEpisodes: GetEpisodesBySceneSeason, falling back to the literal
//     season when no row has that scene season).
//   - Each SxxEyy is looked up as a scene number, per episode number, and read
//     literally only when no row has it (GetStandardEpisodes).
//   - Each absolute number is looked up as a scene absolute, read literally
//     when no row has it -- and also when more than one row does, because
//     Sonarr refuses to pick between several ("Don't allow multiple results
//     without a scene name mapping", GetAnimeEpisodes).
//
// A mapping REPLACES the literal reading rather than adding to it. That is
// the point of it: on a series whose scene season 2 is TVDB's season 1
// episodes 14-26, "S02E05" means TVDB S01E18, and reading it as TVDB S02E05
// as well would approve it for an episode it is not.
//
// Not ported: Sonarr's scene-season lookup by release TITLE
// (SceneMappingService.GetSceneSeasonNumber, "Show S2 - 05"), because
// pkg/release leaves such a season inside the parsed title rather than in
// ParsedRelease.Seasons, and the season-by-title table is TheXEM's separate
// names endpoint.
//
// Three readings go beyond Sonarr's, each for a series whose broadcasts
// hold several of TVDB's episodes (talos, 2026-11-26: Panty & Stocking with
// Garterbelt, thirteen broadcasts of TVDB's thirty season 2 episodes):
//
//   - The season is the scene season the release's TITLE names, when the
//     title is one of the series' scene names for a later season and the
//     release numbers that season 1 (sceneIndex.sceneSeason): "New Panty &
//     Stocking with Garterbelt S01E07" was read as the 2010 season's.
//   - A release that lists numbers after its SxxEyy ("S02E07.14.15") is the
//     scene episode when its row maps onto exactly the listed TVDB episodes
//     -- the list says what the broadcast holds -- and is otherwise every
//     number it names, read literally.
//   - A release that carries the title of the TVDB episode its number names
//     literally ("S02E07.Daten.City.President", TVDB's S02E07) is numbered
//     by TVDB: the scene row for episode 7, the broadcast holding TVDB's
//     episodes 14 and 15, is not it.
func releaseCoverage(p *release.ParsedRelease, scene sceneIndex) coverage {
	c := coverage{pairs: map[seasonEpisode]bool{}, fullSeasons: map[int]bool{}, absolutes: map[int]bool{}}
	if p.FullSeason {
		for _, s := range p.Seasons {
			s = scene.sceneSeason(p.Title, s)
			if eps := scene.bySeason[s]; len(eps) > 0 {
				c.addMapped(eps...)
				continue
			}
			c.fullSeasons[s] = true
		}
	} else {
		for _, s := range p.Seasons {
			s = scene.sceneSeason(p.Title, s)
			for _, e := range p.Episodes {
				eps := scene.byEpisode[seasonEpisode{s, e}]
				switch {
				case len(p.ListedEpisodes) > 0 && len(p.Episodes) == 1:
					if mapsOnto(eps, p.ListedEpisodes) {
						c.addMapped(eps...)
						continue
					}
					c.pairs[seasonEpisode{s, e}] = true
					for _, l := range p.ListedEpisodes {
						c.pairs[seasonEpisode{s, l}] = true
					}
				case len(eps) > 0 && len(p.Episodes) == 1 && scene.titled(s, e, p.EpisodeTitle):
					c.pairs[seasonEpisode{s, e}] = true
				case len(eps) > 0:
					c.addMapped(eps...)
				default:
					c.pairs[seasonEpisode{s, e}] = true
				}
			}
		}
	}
	for _, a := range p.Absolute {
		if eps := scene.byAbsolute[a]; len(eps) == 1 {
			c.addMapped(eps[0])
			continue
		}
		c.absolutes[a] = true
	}
	return c
}

// mapsOnto reports whether a scene episode's rows name exactly the listed
// episode numbers, in one TVDB season.
func mapsOnto(rows []EpisodeNumbering, listed []int) bool {
	if len(rows) == 0 || len(rows) != len(listed) {
		return false
	}
	want := map[int]bool{}
	for _, l := range listed {
		want[l] = true
	}
	for _, r := range rows {
		if r.Season != rows[0].Season || !want[r.Episode] {
			return false
		}
		delete(want, r.Episode)
	}
	return len(want) == 0
}

func (c *coverage) addMapped(eps ...EpisodeNumbering) {
	for _, e := range eps {
		c.pairs[seasonEpisode{e.Season, e.Episode}] = true
		if e.Absolute > 0 {
			c.absolutes[e.Absolute] = true
		}
		c.mapped = append(c.mapped, e)
	}
}

func (c coverage) hasInSeason() bool { return len(c.pairs) > 0 || len(c.fullSeasons) > 0 }

// coversInSeason reports whether every one of the target's episodes is
// covered, by a pack of its season or by its own season and episode.
func (c coverage) coversInSeason(season int, episodes []int) bool {
	if c.fullSeasons[season] {
		return true
	}
	for _, e := range episodes {
		if !c.pairs[seasonEpisode{season, e}] {
			return false
		}
	}
	return true
}

func (c coverage) coversAbsolute(absolutes []int) bool {
	for _, a := range absolutes {
		if !c.absolutes[a] {
			return false
		}
	}
	return true
}

// coversAnyInSeason is a season search's numbering rule (Wave S): a pack of
// the season, or any one of its episodes.
func (c coverage) coversAnyInSeason(season int, episodes []int) bool {
	if c.fullSeasons[season] {
		return true
	}
	for _, e := range episodes {
		if c.pairs[seasonEpisode{season, e}] {
			return true
		}
	}
	return false
}

// coversAnyAbsolute is coversAnyInSeason for the anime absolute numbering:
// any one of the season's absolute numbers.
func (c coverage) coversAnyAbsolute(absolutes []int) bool {
	for _, a := range absolutes {
		if c.absolutes[a] {
			return true
		}
	}
	return false
}

// export is the coverage's exported, sorted form (Decision.Coverage).
func (c coverage) export() Coverage {
	var out Coverage
	for s := range c.fullSeasons {
		out.FullSeasons = append(out.FullSeasons, s)
	}
	slices.Sort(out.FullSeasons)
	for p := range c.pairs {
		out.Episodes = append(out.Episodes, SeasonEpisode{Season: p.season, Episode: p.episode})
	}
	slices.SortFunc(out.Episodes, func(a, b SeasonEpisode) int {
		return cmp.Or(cmp.Compare(a.Season, b.Season), cmp.Compare(a.Episode, b.Episode))
	})
	for a := range c.absolutes {
		out.Absolute = append(out.Absolute, a)
	}
	slices.Sort(out.Absolute)
	return out
}

// CoverageOf is what release p covers in the series' stored numbering,
// read through scene (the series' whole TheXEM table) first and literally
// where it has no row -- the numbering half's own reading
// (releaseCoverage), exported for the search agent's record and the grab
// planner (Wave S).
func CoverageOf(p *release.ParsedRelease, scene []SceneMapping) Coverage {
	if p == nil {
		return Coverage{}
	}
	return releaseCoverage(p, newSceneIndex(scene)).export()
}

// CoverageFor is CoverageOf read with everything id holds of the series'
// numbering: its scene table and the scene season its titles name. It is
// how a download's file is attributed at import, so the file lands on the
// episodes its release was grabbed for (fileimport).
func CoverageFor(p *release.ParsedRelease, id Identity) Coverage {
	if p == nil {
		return Coverage{}
	}
	return releaseCoverage(p, newIdentitySceneIndex(id)).export()
}

// describeMapping is the suffix a rejection message carries when a scene
// lookup changed what the release's numbering means: " (scene numbering;
// TVDB S01E18)". Empty when nothing was mapped.
func (c coverage) describeMapping() string {
	if len(c.mapped) == 0 {
		return ""
	}
	const shown = 4
	parts := make([]string, 0, shown)
	for i, e := range c.mapped {
		if i == shown {
			break
		}
		parts = append(parts, fmt.Sprintf("S%02dE%02d", e.Season, e.Episode))
	}
	s := strings.Join(parts, ", ")
	if n := len(c.mapped) - shown; n > 0 {
		s += fmt.Sprintf(" and %d more", n)
	}
	return " (scene numbering; TVDB " + s + ")"
}
