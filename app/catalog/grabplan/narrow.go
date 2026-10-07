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

package grabplan

import (
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Narrowing (P23): which of a container's items a candidate covers, and
// the episode and issue numbers an entry or a pending grab records for
// them. A pack covering no wanted item is refused (check); one covering
// some is taken as covering those (Decide).

// covers reports an entry covering it: an episode by season and number, an
// issue by number, the owner always.
func covers(e *catalogv1alpha1.DownloadEntry, it Covered) bool {
	switch {
	case it.Issue != "":
		for _, n := range e.Issues {
			if n == it.Issue {
				return true
			}
		}
		return false
	case strings.EqualFold(it.Ref.Kind, "Episode"):
		for _, n := range e.Episodes {
			if n.Season == it.Season && n.Number == it.Number {
				return true
			}
		}
		return false
	}
	return true
}

// coverage is the covered items a candidate names: the owner's one item,
// or the container's items its targets name -- and, for a search answer,
// every episode of the same season its parsed numbers name (a
// multi-episode release found for one of them).
func (d *decider) coverage(c Candidate) []Covered {
	if len(d.v.Covered) == 0 {
		return nil
	}
	if !container(d.v.Kind) {
		return d.v.Covered[:1]
	}
	var out []Covered
	seen := map[string]bool{}
	add := func(it Covered) {
		if !seen[it.Ref.Name] {
			seen[it.Ref.Name] = true
			out = append(out, it)
		}
	}
	for _, t := range c.Targets {
		for _, it := range d.v.Covered {
			if (t.UID != "" && t.UID == it.Ref.UID) || (t.UID == "" && t.Name == it.Ref.Name) {
				add(it)
			}
		}
	}
	if c.MsgID == "" && c.pending < 0 && len(out) > 0 && strings.EqualFold(out[0].Ref.Kind, "Episode") {
		season := out[0].Season
		for _, n := range c.Release.Parsed.Episodes {
			for _, it := range d.v.Covered {
				if it.Season == season && it.Number == n && (len(c.Release.Parsed.Seasons) == 0 || containsInt32(c.Release.Parsed.Seasons, season)) {
					add(it)
				}
			}
		}
	}
	return out
}

func containsInt32(s []int32, v int32) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func container(k commonv1.MediaKind) bool {
	return k == commonv1.MediaKindSeries || k == commonv1.MediaKindComic
}

// numbers is the covered items' episode numbers and issue numbers.
func numbers(items []Covered) ([]catalogv1alpha1.EpisodeNumber, []string) {
	var (
		eps    []catalogv1alpha1.EpisodeNumber
		issues []string
	)
	for _, it := range items {
		switch {
		case it.Issue != "":
			issues = append(issues, clamp(it.Issue, 16))
		case strings.EqualFold(it.Ref.Kind, "Episode"):
			eps = append(eps, catalogv1alpha1.EpisodeNumber{Season: it.Season, Number: it.Number})
		}
	}
	if len(eps) > maxCovered {
		eps = eps[:maxCovered]
	}
	if len(issues) > maxCovered {
		issues = issues[:maxCovered]
	}
	return eps, issues
}
