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

package torrent

import (
	"slices"
	"time"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/release"
)

// EpisodeNumber is one season/episode pair a selection accepts.
type EpisodeNumber struct {
	Season  int `json:"season"`
	Episode int `json:"episode"`
}

// Selection is the persisted form of a torrent's file selection: every
// identity a file of a wanted episode may carry. It is resolved from the
// catalog once, when the transfer is first added, and saved in the
// re-attach descriptor, so [Engine.ReAttach] rebuilds the identical
// [download.FileSelector] before the manager -- and any catalog read --
// is running.
//
// Each wanted episode contributes its official season/episode, its scene
// season/episode and absolute numbers when the catalog has them, and its air
// date with a day either side. A pack names its files in whichever
// numbering its release group used, and that is not knowable here, so the
// selection accepts all of them: a file matching an unwanted episode's
// number in one scheme and a wanted episode's in another is fetched. Wider
// is always the safe direction -- an extra episode costs bandwidth, a missed
// one costs the grab.
type Selection struct {
	Episodes  []EpisodeNumber `json:"episodes,omitempty"`
	Absolutes []int           `json:"absolutes,omitempty"`
	AirDates  []string        `json:"airDates,omitempty"`
}

// empty reports whether s would accept nothing, i.e. is not a selection.
func (s *Selection) empty() bool {
	return s == nil || (len(s.Episodes) == 0 && len(s.Absolutes) == 0 && len(s.AirDates) == 0)
}

// selectionFrom is the file selection an engine command carries
// (ADR-0019 §6.7: cmd.Selection replaces the engine's Episode reads): the
// covered episodes' numbers, absolute numbers and air dates, as the manager
// read them from its cache. nil wants every file.
func selectionFrom(sel *schema.TransferSelection) *Selection {
	if sel == nil {
		return nil
	}
	out := &Selection{AirDates: append([]string(nil), sel.AirDates...)}
	for _, e := range sel.Episodes {
		out.Episodes = append(out.Episodes, EpisodeNumber{Season: int(e.Season), Episode: int(e.Number)})
	}
	for _, a := range sel.Absolutes {
		out.Absolutes = append(out.Absolutes, int(a))
	}
	out.normalize()
	if out.empty() {
		return nil
	}
	return out
}

// normalize sorts and de-duplicates, so the persisted form is stable.
func (s *Selection) normalize() {
	slices.SortFunc(s.Episodes, func(a, b EpisodeNumber) int {
		if a.Season != b.Season {
			return a.Season - b.Season
		}
		return a.Episode - b.Episode
	})
	s.Episodes = slices.Compact(s.Episodes)
	slices.Sort(s.Absolutes)
	s.Absolutes = slices.Compact(s.Absolutes)
	slices.Sort(s.AirDates)
	s.AirDates = slices.Compact(s.AirDates)
}

// selector renders s as the predicate AddRequest.WantFile takes, or nil to
// want every file.
func (s *Selection) selector() download.FileSelector {
	if s.empty() {
		return nil
	}
	episodes := make(map[EpisodeNumber]struct{}, len(s.Episodes))
	for _, e := range s.Episodes {
		episodes[e] = struct{}{}
	}
	absolutes := make(map[int]struct{}, len(s.Absolutes))
	for _, a := range s.Absolutes {
		absolutes[a] = struct{}{}
	}
	airDates := make(map[string]struct{}, len(s.AirDates))
	for _, d := range s.AirDates {
		airDates[d] = struct{}{}
	}
	return func(path string, _ int64) bool {
		return wantFile(path, episodes, absolutes, airDates)
	}
}

// wantFile decides one file. It skips a file only when the file names an
// episode positively and that episode is not wanted; a file that names no
// episode it can read -- an .nfo, a featurette, a parse failure -- is kept,
// because skipping is the direction that can lose the grab.
func wantFile(path string, episodes map[EpisodeNumber]struct{}, absolutes map[int]struct{}, airDates map[string]struct{}) bool {
	p, err := release.ParsePath(path, release.Options{Kind: commonv1alpha1.MediaKindEpisode})
	if err != nil || p == nil {
		return true
	}

	identified := false
	if p.AirDate != nil && len(airDates) > 0 {
		identified = true
		if _, ok := airDates[p.AirDate.UTC().Format(time.DateOnly)]; ok {
			return true
		}
	}
	if len(p.Seasons) == 1 && len(p.Episodes) > 0 && !p.FullSeason {
		identified = true
		for _, e := range p.Episodes {
			if _, ok := episodes[EpisodeNumber{Season: p.Seasons[0], Episode: e}]; ok {
				return true
			}
		}
	}
	if len(p.Absolute) > 0 && len(absolutes) > 0 {
		identified = true
		for _, a := range p.Absolute {
			if _, ok := absolutes[a]; ok {
				return true
			}
		}
	}
	return !identified
}
