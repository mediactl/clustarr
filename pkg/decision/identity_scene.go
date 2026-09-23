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
	"fmt"
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
}

type seasonEpisode struct{ season, episode int }

// sceneIndex is Identity.SceneMappings keyed for lookup, built once per
// Evaluate. The zero value maps nothing.
type sceneIndex struct {
	byEpisode  map[seasonEpisode][]EpisodeNumbering
	bySeason   map[int][]EpisodeNumbering
	byAbsolute map[int][]EpisodeNumbering
}

func newSceneIndex(rows []SceneMapping) sceneIndex {
	if len(rows) == 0 {
		return sceneIndex{}
	}
	idx := sceneIndex{
		byEpisode:  map[seasonEpisode][]EpisodeNumbering{},
		bySeason:   map[int][]EpisodeNumbering{},
		byAbsolute: map[int][]EpisodeNumbering{},
	}
	for _, r := range rows {
		if r.TVDB.Episode <= 0 {
			// A row that names no TVDB episode maps a scene number onto
			// nothing; reading it as a mapping would make that scene number
			// unmatchable instead of falling back to its literal reading.
			continue
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
func releaseCoverage(p *release.ParsedRelease, scene sceneIndex) coverage {
	c := coverage{pairs: map[seasonEpisode]bool{}, fullSeasons: map[int]bool{}, absolutes: map[int]bool{}}
	if p.FullSeason {
		for _, s := range p.Seasons {
			if eps := scene.bySeason[s]; len(eps) > 0 {
				c.addMapped(eps...)
				continue
			}
			c.fullSeasons[s] = true
		}
	} else {
		for _, s := range p.Seasons {
			for _, e := range p.Episodes {
				if eps := scene.byEpisode[seasonEpisode{s, e}]; len(eps) > 0 {
					c.addMapped(eps...)
					continue
				}
				c.pairs[seasonEpisode{s, e}] = true
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
