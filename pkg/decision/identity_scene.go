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
	"unicode"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
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
	byAbsolute map[int][]absoluteRow
	// titles is each TVDB episode's title key, where the rows carry one;
	// seasonNames the scene season each of the series' scene titles names,
	// and spelledNames the same for the names whose key two seasons share,
	// under their spelling (addSeasonNames).
	titles       map[seasonEpisode]string
	seasonNames  map[string]int
	spelledNames map[string]int
	// ownTitles keys the titles the series itself is known by
	// (Identity.Titles, and its scene names for every season at once),
	// empty when the identity carries none: see places.
	ownTitles map[string]struct{}
}

// newIdentitySceneIndex is an Identity's scene table and scene season
// names, keyed for lookup.
func newIdentitySceneIndex(id Identity) sceneIndex {
	idx := newSceneIndex(id.SceneMappings)
	idx.addSeasonNames(id.SceneSeasonNames)
	if len(id.Titles) > 0 {
		idx.ownTitles = targetTitleKeys(common.MediaKindEpisode, id)
		for _, n := range id.SceneSeasonNames {
			if k := titleKey(n.Title); k != "" && n.Season < 1 {
				idx.ownTitles[k] = struct{}{}
			}
		}
	}
	return idx
}

// places reports whether a bare number under p's title can be placed in the
// series: an anime release's "- 02", which says nothing of its season.
//
// On a series whose seasons have scene names of their own, the title is
// what says which season such a number counts in: one of those names is
// that season, and the series' own title its first (sceneSeason). A title
// that is neither names some part of the series the tables do not know -- a
// cour ("JoJo no Kimyou na Bouken - Stone Ocean Part 2 - 01 ~ 12", episodes
// 13-24 of season 5's name), or a season under another spelling of its
// name ("JoJo's Bizarre Adventure: Golden Wind (Part 5) - 02", season 4)
// -- and such a release reaches the numbering at all only because an
// indexer asked by the series' id returned it, with no title check
// (Identity.IDQueryIndexers). Its number was read as the series' own, the
// first season's: both were approved for S01E02, and the first was taken
// for S01E02 and S01E03 twice in two minutes, displacing a live download
// of the right episode (talos, 2026-11-27; owner: "Ensure that only the
// correct season is selected. Either Season 1 or 'Phantom Blood & Battle
// Tendency'"). Such a number is now left unplaced: not mapped, and not read
// literally either.
//
// A series with no scene season names, and an identity that carries no
// titles to know its own by, read every number as before.
func (idx sceneIndex) places(p *release.ParsedRelease) bool {
	if !idx.namesSeasons() || len(idx.ownTitles) == 0 {
		return true
	}
	titles := p.Titles
	if len(titles) == 0 {
		titles = []string{p.Title}
	}
	for _, t := range titles {
		k := titleKey(t)
		if _, ok := idx.ownTitles[k]; ok {
			return true
		}
		if _, ok := idx.seasonNames[k]; ok {
			return true
		}
		if _, ok := idx.spelledNames[spelledKey(t)]; ok {
			return true
		}
	}
	return false
}

// addSeasonNames keys the scene season each of names is the name of. A key
// is a title with its punctuation gone (titleKey), and two seasons' names
// can differ in nothing else: TheXEM's "New Game!" is season 1 and "New
// Game!!" season 2. Under the one key the later name won, so every season 1
// release of that series was read as season 2's ("[DB]New Game! - 09" as
// S02E09, found replaying talos' files, 2026-11-27). Such names are kept
// under their spelling instead (spelledKey), and a title that is neither
// spelling names no season.
func (idx *sceneIndex) addSeasonNames(names []SceneSeasonName) {
	seasons := map[string]map[int]bool{}
	for _, n := range names {
		if k := titleKey(n.Title); k != "" && n.Season >= 1 {
			if seasons[k] == nil {
				seasons[k] = map[int]bool{}
			}
			seasons[k][n.Season] = true
		}
	}
	for _, n := range names {
		if n.Season < 1 {
			continue // a name for every season names none (Identity.SceneSeasonNames)
		}
		k := titleKey(n.Title)
		switch len(seasons[k]) {
		case 0:
		case 1:
			if idx.seasonNames == nil {
				idx.seasonNames = map[string]int{}
			}
			idx.seasonNames[k] = n.Season
		default:
			if idx.spelledNames == nil {
				idx.spelledNames = map[string]int{}
			}
			sk := spelledKey(n.Title)
			if prior, ok := idx.spelledNames[sk]; ok && prior != n.Season {
				// One spelling for two seasons says nothing.
				idx.spelledNames[sk] = 0
				continue
			}
			idx.spelledNames[sk] = n.Season
		}
	}
}

// spelledKey is a title as it is spelled, apart from its case and how its
// words are separated: what tells "New Game!" from "New Game!!".
func spelledKey(title string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return unicode.IsSpace(r) || r == '.' || r == '_'
	}), " ")
}

// namedSeason is the scene season title is the series' name of, 0 when it
// is the name of none.
func (idx sceneIndex) namedSeason(title string) int {
	if n, ok := idx.seasonNames[titleKey(title)]; ok {
		return n
	}
	return idx.spelledNames[spelledKey(title)]
}

// namesSeasons reports whether the series has scene names for its seasons.
func (idx sceneIndex) namesSeasons() bool {
	return len(idx.seasonNames) > 0 || len(idx.spelledNames) > 0
}

// sceneSeason is the scene season a release's season number means under
// its title: a title that is one of the series' scene names for a later
// season numbers its own first season from there. Any other season number
// under such a title, and any title that is not one, is read as written.
func (idx sceneIndex) sceneSeason(title string, season int) int {
	if n := idx.namedSeason(title); n > 1 && season == 1 {
		return n
	}
	return season
}

// NamedSeason is the season p's own title names: the marker it ends in
// ("Show S2 - 05", release.ParsedRelease.TitleSeason) or, under a title
// that is one of the series' scene names for a later season ("Date A Live
// II - 07"), that season. 0 when the title names none: the series' own
// title, or its first season's name. A number under such a title says
// nothing of its season (fileimport reads it by its download's).
func NamedSeason(p *release.ParsedRelease, id Identity) int {
	if p == nil {
		return 0
	}
	if p.TitleSeason > 1 {
		return p.TitleSeason
	}
	var idx sceneIndex
	idx.addSeasonNames(id.SceneSeasonNames)
	if n := idx.namedSeason(p.Title); n > 1 {
		return n
	}
	return 0
}

// absoluteRow is one row of the table under its scene absolute number: the
// scene episode it is a row of, and the TVDB episode it names.
type absoluteRow struct {
	scene seasonEpisode
	tvdb  EpisodeNumbering
}

// absolute is the TVDB episodes the scene absolute number a names in a
// release titled title, nil when no row has it or the table cannot say
// which rows are meant.
//
// The rows of one scene episode are that episode: a broadcast TVDB lists as
// two episodes has two rows under its one absolute number, and it is both.
// Rows of several scene episodes share a number when the scene counts each
// season from 1 (TheXEM on Panty & Stocking with Garterbelt: absolute 8 is
// the 2010 season's eighth broadcast and the 2025 season's). Then the
// release's title says which season, where the series' scene names tell its
// seasons apart (sceneSeason: the series' own title is its first season, a
// later season's name that season). Without such names nothing does, and
// the number is left unmapped, as Sonarr leaves it ("Don't allow multiple
// results without a scene name mapping", GetAnimeEpisodes).
func (idx sceneIndex) absolute(title string, a int) []EpisodeNumbering {
	rows := idx.byAbsolute[a]
	if len(rows) == 0 {
		return nil
	}
	if !oneSceneEpisode(rows) {
		if !idx.namesSeasons() {
			return nil
		}
		season := idx.sceneSeason(title, 1)
		rows = slices.DeleteFunc(slices.Clone(rows), func(r absoluteRow) bool { return r.scene.season != season })
		if len(rows) == 0 || !oneSceneEpisode(rows) {
			return nil
		}
	}
	out := make([]EpisodeNumbering, len(rows))
	for i, r := range rows {
		out[i] = r.tvdb
	}
	return out
}

// oneSceneEpisode reports whether rows are all rows of one scene episode.
func oneSceneEpisode(rows []absoluteRow) bool {
	for _, r := range rows[1:] {
		if r.scene != rows[0].scene {
			return false
		}
	}
	return true
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
		byAbsolute: map[int][]absoluteRow{},
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
			idx.byAbsolute[r.Scene.Absolute] = append(idx.byAbsolute[r.Scene.Absolute],
				absoluteRow{scene: seasonEpisode{r.Scene.Season, r.Scene.Episode}, tvdb: r.TVDB})
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
	// unplaced is set when the release's bare numbers were left out because
	// its title places them nowhere in the series (sceneIndex.places).
	unplaced bool
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
//     when no row has it -- and also when several scene episodes do and the
//     title does not say which, because Sonarr refuses to pick between
//     several ("Don't allow multiple results without a scene name mapping",
//     GetAnimeEpisodes). sceneIndex.absolute has what the title can say.
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
	placed := len(p.Absolute) == 0 || scene.places(p)
	for _, a := range p.Absolute {
		if p.TitleSeason > 1 && c.addTitleSeason(p, scene, a) {
			continue
		}
		if !placed {
			c.unplaced = true
			continue
		}
		if eps := scene.absolute(p.Title, a); len(eps) > 0 {
			c.addMapped(eps...)
			continue
		}
		c.absolutes[a] = true
	}
	return c
}

// addTitleSeason reads a, the number after a title that ends in a season
// marker ("Kingdom S5 - 06", "Seirei Gensouki S2 - 01",
// release.ParsedRelease.TitleSeason), as that season's episode: fansubs
// number such a release inside the season its title names, as Sonarr reads
// an absolute number under a title that maps to a season (ParsingService's
// GetAnimeEpisodes: the scene season and the number first, the absolute
// number last). The season is the one the series' scene names give the
// title where they know it, and the marker's otherwise.
//
// It answers false, leaving the number an absolute one, only when the scene
// table has no such episode of the season and does hold the number as an
// absolute: a group that counts on through the series under a season's
// name ("Shingeki no Kyojin S3 - 49"). With no row either way the number is
// the season's, never also the series' absolute: read both ways, "Show S2 -
// 05" would be taken for season 1's fifth episode as well.
//
// Before this the number was an absolute one whatever the title said, so a
// pack's "Pop Team Epic S2 - 01" was filed at import as S01E01 and held
// against the file there (talos, 2026-11-27).
func (c *coverage) addTitleSeason(p *release.ParsedRelease, scene sceneIndex, a int) bool {
	season := scene.sceneSeason(p.Title, 1)
	if season == 1 {
		season = p.TitleSeason
	}
	if eps := scene.byEpisode[seasonEpisode{season, a}]; len(eps) > 0 {
		c.addMapped(eps...)
		return true
	}
	if len(scene.byAbsolute[a]) > 0 {
		return false
	}
	c.pairs[seasonEpisode{season, a}] = true
	return true
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

// addMapped adds the TVDB episodes a scene row names, by their place alone:
// the season and episode. The row's own absolute number is TheXEM's count,
// and what a coverage's absolute numbers are compared with is the series'
// stored one (Episode.status.absoluteNumber, from the metadata provider);
// the two disagree on some series, and a number that names one episode in
// TheXEM's count names another in the series'. The importer attributes a
// file the same way (fileimport.MatchDownloaded: CoverageFor's places), so
// a release is grabbed for the episode its file will be filed as.
//
// Until 2026-11-27 the row's absolute number went into the coverage too. On
// talos "[Erai-raws] Shiguang Dailiren Season 3 - 05", the scene's name for
// Link Click's S04E05 (TheXEM absolute 34; the series stores 29), was taken
// for S03E10, which the series stores as absolute 34 and which was missing:
// four releases of it in four minutes, each filed at import as S04E05 and
// held against the transcoded file there.
func (c *coverage) addMapped(eps ...EpisodeNumbering) {
	for _, e := range eps {
		c.pairs[seasonEpisode{e.Season, e.Episode}] = true
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
	out.Unplaced = c.unplaced
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
