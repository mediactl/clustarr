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
	"context"
	"fmt"
	"slices"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
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

// resolveSelection reads the Episodes a Download targets and returns the
// selection that fetches only their files. It returns nil -- want every
// file -- for anything that is not an episode or a pack of episodes, and
// for any Episode it cannot read: a selection built from part of a pack
// would skip the files of the episodes it could not see.
//
// A movie, an album, a book or a comic issue is left whole. Their releases
// are one item, and the files beside the main one (a cue sheet, a cover,
// subtitles) belong to it.
func resolveSelection(ctx context.Context, r client.Reader, dl downloadTarget) (*Selection, error) {
	if r == nil {
		return nil, nil
	}
	var names []string
	switch dl.target.Kind {
	case commonv1alpha1.MediaKindEpisode:
		names = []string{dl.target.Name}
	case commonv1alpha1.MediaKindSeries:
		names = dl.target.Keys
	default:
		return nil, nil
	}
	if len(names) == 0 {
		return nil, nil
	}

	sel := &Selection{}
	for _, name := range names {
		var ep catalogv1alpha1.Episode
		if err := r.Get(ctx, client.ObjectKey{Namespace: dl.namespace, Name: name}, &ep); err != nil {
			return nil, fmt.Errorf("torrent: read episode %s/%s for file selection: %w", dl.namespace, name, err)
		}
		sel.add(&ep)
	}
	sel.normalize()
	return sel, nil
}

// downloadTarget is the part of a Download [resolveSelection] reads.
type downloadTarget struct {
	namespace string
	target    commonv1alpha1.MediaRef
}

func (s *Selection) add(ep *catalogv1alpha1.Episode) {
	s.Episodes = append(s.Episodes, EpisodeNumber{Season: int(ep.Spec.SeasonNumber), Episode: int(ep.Spec.EpisodeNumber)})
	if ep.Status.AbsoluteNumber != nil {
		s.Absolutes = append(s.Absolutes, int(*ep.Status.AbsoluteNumber))
	}
	if sn := ep.Status.SceneNumbering; sn != nil {
		if sn.Episode != nil {
			season := ep.Spec.SeasonNumber
			if sn.Season != nil {
				season = *sn.Season
			}
			s.Episodes = append(s.Episodes, EpisodeNumber{Season: int(season), Episode: int(*sn.Episode)})
		}
		if sn.Absolute != nil {
			s.Absolutes = append(s.Absolutes, int(*sn.Absolute))
		}
	}
	if ep.Status.AirDate != nil {
		// UTC, never the local zone metav1.Time decodes into (CLAUDE.md's
		// New Year gotcha), and a day either side, because a release names
		// the local broadcast date and a provider's date may be the UTC one.
		day := ep.Status.AirDate.UTC()
		for _, d := range []int{-1, 0, 1} {
			s.AirDates = append(s.AirDates, day.AddDate(0, 0, d).Format(time.DateOnly))
		}
	}
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
