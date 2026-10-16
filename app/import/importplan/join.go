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

package importplan

import (
	"path/filepath"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// PartNumber is the part number path's file name carries -- Part1,
// Part.2, pt3, CD1, Disc 2 (release.PartOf) -- false for none.
func PartNumber(path string) (int, bool) {
	_, n, ok := release.PartOf(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	return n, ok
}

// foldParts folds each movie's parts among cs -- two or more of its files
// numbered 1 to n (partNumber) whose probes agree on the video -- into one
// candidate that joins them in order (candidate.join), its probe the
// parts' length end to end: a movie is one file, and a release of one
// encode in parts otherwise imported only the part ranked first (28 Up,
// 42 Up and 49 Up, 2026-10-16). Anything else is left as it is: a lone
// file whose title says Part 1, numbers that skip, parts of other videos.
func foldParts(cs []candidate) []candidate {
	byItem := map[string][]int{}
	for i, c := range cs {
		if c.kind == commonv1.MediaKindMovie && len(c.keys) == 1 {
			if _, ok := partOf(c); ok {
				byItem[c.keys[0]] = append(byItem[c.keys[0]], i)
			}
		}
	}
	folded := map[int]bool{}
	var joined []candidate
	for _, idx := range byItem {
		parts, ok := orderedParts(cs, idx)
		if !ok {
			continue
		}
		j := cs[parts[0]]
		for _, i := range parts {
			j.join = append(j.join, cs[i].f.Path)
			folded[i] = true
		}
		if p := j.f.Probe; p != nil {
			sum := *p
			sum.DurationSeconds = 0
			for _, i := range parts {
				sum.DurationSeconds += cs[i].f.Probe.DurationSeconds
			}
			j.f.Probe = &sum
		}
		for _, i := range parts[1:] {
			j.f.SizeBytes += cs[i].f.SizeBytes
		}
		joined = append(joined, j)
	}
	if len(joined) == 0 {
		return cs
	}
	out := make([]candidate, 0, len(cs))
	for i, c := range cs {
		if !folded[i] {
			out = append(out, c)
		}
	}
	return append(out, joined...)
}

// orderedParts is idx -- candidates of one movie, each numbered -- ordered
// by part, when they are parts 1 to n of one encode: two or more, each
// number once, none skipped, every probe present and agreeing on the video
// codec, frame size and HDR, since only the parts of one encode join.
func orderedParts(cs []candidate, idx []int) ([]int, bool) {
	if len(idx) < 2 {
		return nil, false
	}
	byNum := map[int]int{}
	for _, i := range idx {
		n, _ := partOf(cs[i])
		if _, dup := byNum[n]; dup {
			return nil, false
		}
		byNum[n] = i
	}
	out := make([]int, 0, len(idx))
	for n := 1; n <= len(idx); n++ {
		i, ok := byNum[n]
		if !ok {
			return nil, false
		}
		out = append(out, i)
	}
	first := cs[out[0]].f.Probe
	for _, i := range out {
		p := cs[i].f.Probe
		if p == nil || first == nil || p.VideoCodec != first.VideoCodec || p.Width != first.Width ||
			p.Height != first.Height || p.HDR != first.HDR {
			return nil, false
		}
	}
	return out, true
}

// partOf is c's part: the set part whose payload holds it, when the import
// is a set's (InspectedFile.Part, which an obfuscated name needs), else
// the number its file name carries.
func partOf(c candidate) (int, bool) {
	if c.f.Part > 0 {
		return int(c.f.Part), true
	}
	return PartNumber(c.f.Path)
}

// setRejection refuses a set's movie file that is not every part joined
// (a release fault): a set imports as the whole film or not at all, never
// as the part that inspected alone. "" for any other import.
func (p *planner) setRejection(rel string, c candidate) Rejection {
	e := &p.in.Entry
	if e.Set == nil {
		return Rejection{}
	}
	if n := len(c.join); n != int(e.Set.Of) {
		return releaseFaultRejection("%s: the set's %d parts did not join (%d did): a part is missing, unreadable or of another encode",
			rel, e.Set.Of, n)
	}
	return Rejection{}
}

// joinedRejection refuses a joined movie running under JoinedShortPercent
// of the movie: parts joined are meant to be the whole film, and a short
// one is missing a part (the release's fault). It fails open without a
// probe, a duration or a metadata runtime.
func joinedRejection(rel string, c candidate, runtimeMinutes int) Rejection {
	probe := c.f.Probe
	if len(c.join) == 0 || probe == nil || probe.DurationSeconds <= 0 || runtimeMinutes <= 0 {
		return Rejection{}
	}
	runs := int(probe.DurationSeconds / 60)
	if runs*100 < runtimeMinutes*JoinedShortPercent {
		return releaseFaultRejection("%s: its %d parts joined run %d minutes, under %d%% of the movie's %d: a part is missing",
			rel, len(c.join), runs, JoinedShortPercent, runtimeMinutes)
	}
	return Rejection{}
}
