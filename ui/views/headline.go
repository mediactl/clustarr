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

package views

import (
	"fmt"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// headline is a release's or a file's name as a person reads it
// (2026-11-28, the Liquid Glass redesign: an Activity row leads with what
// it is, "One Piece", not "One.Piece.S17.REPACK.1080p..."): the title, a
// movie's with its year, and a line of what the release is -- its season
// and episode, the episode's title, its quality and its group. A name the
// parser cannot read is its own headline with no line.
func headline(name string, kind commonv1.MediaKind) (string, string) {
	p, err := release.ParseKind(name, kind)
	if err != nil || p == nil || strings.TrimSpace(p.Title) == "" {
		return name, ""
	}
	head := p.Title
	var sub []string
	switch {
	case len(p.Seasons) > 0 && len(p.Episodes) > 0:
		sub = append(sub, fmt.Sprintf("S%02dE%02d", p.Seasons[0], p.Episodes[0]))
	case len(p.Seasons) > 1:
		sub = append(sub, fmt.Sprintf("Seasons %d–%d", p.Seasons[0], p.Seasons[len(p.Seasons)-1]))
	case len(p.Seasons) == 1:
		sub = append(sub, fmt.Sprintf("Season %d", p.Seasons[0]))
	case len(p.Episodes) > 0:
		sub = append(sub, fmt.Sprintf("Episode %d", p.Episodes[0]))
	case p.Year > 0:
		head = fmt.Sprintf("%s (%d)", head, p.Year)
	}
	if p.EpisodeTitle != "" {
		sub = append(sub, p.EpisodeTitle)
	}
	if q := p.Quality.Name; q != "" && q != "Unknown" {
		sub = append(sub, q)
	}
	if p.Group != "" {
		sub = append(sub, p.Group)
	}
	return head, strings.Join(sub, " · ")
}
