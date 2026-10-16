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
	"regexp"
	"strconv"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// partRE is a part number in a file name: Part1, Part.2, pt3, CD1, Disc 2.
var partRE = regexp.MustCompile(`(?i)(?:^|[ ._\-\[(])(?:part|pt|cd|disc|disk)[ ._\-]?0?([1-9])(?:$|[ ._\-\])])`)

// PartNumber is the part number path's file name carries -- Part1,
// Part.2, pt3, CD1, Disc 2 -- false for none.
func PartNumber(path string) (int, bool) {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	m := partRE.FindAllStringSubmatch(base, -1)
	if len(m) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(m[len(m)-1][1])
	return n, err == nil
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
			if _, ok := PartNumber(c.f.Path); ok {
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
		n, _ := PartNumber(cs[i].f.Path)
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
