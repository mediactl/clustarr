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

package segments

import (
	"slices"
	"time"

	"github.com/dlclark/regexp2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// The chapter-name patterns are Intro Skipper's defaults, verbatim
// (intro-skipper/intro-skipper IntroSkipper/Configuration/PluginConfiguration.cs,
// master, read 2026-10-01). Their negative lookahead -- "Opening End" is not
// an intro -- needs regexp2; RE2 has none.
const (
	introPattern   = `(^|\s)(Intro|Introduction|OP|Opening)(?![\s:]+End)(\s|:|$)`
	creditsPattern = `(^|\s)(Credits?|ED|Ending|Outro)(?![\s:]+End)(\s|:|$)`
	previewPattern = `(^|\s)(Preview|PV|Sneak\s?Peek|Coming\s?(Up|Soon)|Next\s+(time|on|episode)|Extra|Teaser|Trailer)(?![\s:]+End)(\s|:|$)`
	recapPattern   = `(^|\s)(Re?cap|Sum{1,2}ary|Prev(ious(ly)?)?|(Last|Earlier)(\s\w+)?|Catch[ -]up)(?![\s:]+End)(\s|:|$)`
)

// creditsTitle is a chapter title that is wholly a credits name. Such a
// chapter was authored for this file's own timeline, so it outranks
// TheIntroDB for credits (Merge); a title the credits pattern only matches
// within ("Credits Song", "ED 2") does not. Of 107 files with both on the
// owner's library (2026-10-01), 36 disagreed by more than 5 s, the chapter
// right in the worst cases (Game of Thrones S02E09: TheIntroDB mid-battle,
// 8:44 early; Arcane S01E07: 2:03 late). A bare "ED", anime's own name for
// its ending, is one since 2026-11-09 (owner): YakuboEncodes' To LOVE-Ru
// Blu-rays title it so, beside TheIntroDB credits covering the whole episode.
var creditsTitle = compile(`^\s*((End|Closing)\s+)?Credits?\s*$|^\s*Ending\s*$|^\s*Outro\s*$|^\s*ED\s*$`)

// openingTitle names the opening itself: an "OP" or "Opening" chapter. A
// chapter the intro pattern matches without it ("Intro", "Introduction")
// beside one is the cold open, which Merge leaves out (ColdOpens; owner,
// 2026-11-10: YakuboEncodes' To LOVE-Ru Blu-rays chapter S01E01 as "Intro"
// 0:00-1:29, the cold open, then "OP" 1:29-2:59, and both read as intros,
// so Plex offered "Skip intro" over the cold open).
var openingTitle = compile(`(^|\s)(OP|Opening)(?![\s:]+End)(\s|:|$)`)

// Chapter confidences: ExactCreditsConfidence for a chapter whose whole
// title is a credits name, chapterConfidence for any other match.
const (
	ExactCreditsConfidence int32 = 100
	chapterConfidence      int32 = 90
)

type chapterRule struct {
	kind Kind
	re   *regexp2.Regexp
}

// chapterRules are tried in this order; a chapter takes the first match.
var chapterRules = []chapterRule{
	{KindIntro, compile(introPattern)},
	{KindRecap, compile(recapPattern)},
	{KindCredits, compile(creditsPattern)},
	{KindPreview, compile(previewPattern)},
}

func compile(p string) *regexp2.Regexp {
	re := regexp2.MustCompile(p, regexp2.IgnoreCase)
	re.MatchTimeout = 100 * time.Millisecond
	return re
}

// FromChapters returns a segment for each chapter whose name says what it
// is -- confidence 100 for a credits chapter wholly titled as credits, 90
// for any other -- trusted by kind: a movie's chapters give credits
// only, since a film's scene names ("Opening Night", "The Last Stand") read
// as intros and recaps, and a preview must start after the file's midpoint,
// since a TV "Teaser" is the cold open. Every intro-named chapter is given;
// which of two is the opening is Merge's call (ColdOpens), made from the
// chapter titles at every merge rather than here, so a file is not analysed
// again when that rule moves.
func FromChapters(ch []commonv1.Chapter, movie bool, durationMs int64) []Segment {
	var out []Segment
	for _, c := range ch {
		if c.EndMillis <= c.StartMillis {
			continue
		}
		for _, r := range chapterRules {
			if movie && r.kind != KindCredits {
				continue
			}
			if r.kind == KindPreview && c.StartMillis*2 < durationMs {
				continue
			}
			if ok, err := r.re.MatchString(c.Title); err == nil && ok {
				conf := chapterConfidence
				if r.kind == KindCredits {
					if exact, err := creditsTitle.MatchString(c.Title); err == nil && exact {
						conf = ExactCreditsConfidence
					}
				}
				out = append(out, Segment{
					Kind: r.kind, StartMs: c.StartMillis, EndMs: c.EndMillis,
					Source: SourceChapters, Confidence: conf,
				})
				break
			}
		}
	}
	return out
}

// ColdOpens are s's chapter intro segments that are a cold open rather
// than the opening: those whose chapter -- the one starting where the
// segment does -- is intro-named without naming the opening (OP, Opening),
// when another chapter intro of s names it. Without such a chapter, or
// without ch, every intro chapter stands: a lone "Intro" is the intro.
func ColdOpens(s []Segment, ch []commonv1.Chapter) []Segment {
	titles := make(map[int64]string, len(ch))
	for _, c := range ch {
		titles[c.StartMillis] = c.Title
	}
	opening := false
	var cold []Segment
	for _, x := range s {
		if x.Kind != KindIntro || x.Source != SourceChapters {
			continue
		}
		title, ok := titles[x.StartMs]
		if !ok {
			continue
		}
		if named, err := openingTitle.MatchString(title); err == nil && named {
			opening = true
		} else {
			cold = append(cold, x)
		}
	}
	if !opening {
		return nil
	}
	return cold
}

// WithoutColdOpens is s less ColdOpens(s, ch).
func WithoutColdOpens(s []Segment, ch []commonv1.Chapter) []Segment {
	cold := ColdOpens(s, ch)
	if len(cold) == 0 {
		return s
	}
	out := make([]Segment, 0, len(s)-len(cold))
	for _, x := range s {
		if !slices.Contains(cold, x) {
			out = append(out, x)
		}
	}
	return out
}
