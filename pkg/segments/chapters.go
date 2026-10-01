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
	"time"

	"github.com/dlclark/regexp2"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
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

type chapterRule struct {
	kind catalogv1alpha1.MarkerKind
	re   *regexp2.Regexp
}

// chapterRules are tried in this order; a chapter takes the first match.
var chapterRules = []chapterRule{
	{catalogv1alpha1.MarkerIntro, compile(introPattern)},
	{catalogv1alpha1.MarkerRecap, compile(recapPattern)},
	{catalogv1alpha1.MarkerCredits, compile(creditsPattern)},
	{catalogv1alpha1.MarkerPreview, compile(previewPattern)},
}

func compile(p string) *regexp2.Regexp {
	re := regexp2.MustCompile(p, regexp2.IgnoreCase)
	re.MatchTimeout = 100 * time.Millisecond
	return re
}

// FromChapters returns a segment, confidence 100, for each chapter whose
// name says what it is.
func FromChapters(ch []commonv1.Chapter) []Segment {
	var out []Segment
	for _, c := range ch {
		if c.EndMillis <= c.StartMillis {
			continue
		}
		for _, r := range chapterRules {
			if ok, err := r.re.MatchString(c.Title); err == nil && ok {
				out = append(out, Segment{
					Kind: r.kind, StartMs: c.StartMillis, EndMs: c.EndMillis,
					Source: catalogv1alpha1.SegmentSourceChapters, Confidence: 100,
				})
				break
			}
		}
	}
	return out
}
