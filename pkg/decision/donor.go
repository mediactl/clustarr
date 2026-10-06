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
	"context"
	"slices"
	"strings"
	"time"

	"github.com/dlclark/regexp2"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/release"
)

// multiLanguageRegex is a title that says it carries more than one
// language without naming them: a bare DL ("dual language", as BoB's
// "AAC.DL") that is not WEB-DL's, or MULTi.
var multiLanguageRegex = func() *regexp2.Regexp {
	re := regexp2.MustCompile(`(?<!WEB[ ._-]?)\bDL\b|\bMULTI\b`, regexp2.IgnoreCase)
	re.MatchTimeout = 100 * time.Millisecond
	return re
}()

// evaluateDonor finishes evaluateOne for an audio donor (Target.Donor):
// the shared checks already ran into rejections; the donor's own are its
// languages, the item's rejected donors, a donor in flight, sample and
// blocklist. Quality, cutoff, upgrade and transcoded are not a donor's
// business.
func evaluateDonor(t Target, originalLanguage string, need []string, p quality.Profile, parsed *release.ParsedRelease,
	rel common.ReleaseInfo, score int, matched []string, rejections []common.Rejection,
) Decision {
	add := func(r *common.Rejection) {
		if r != nil {
			rejections = append(rejections, *r)
		}
	}
	add(donorLanguageRejection(need, originalLanguage, p, parsed, rel.Title))
	if slices.Contains(t.Donor.Rejected, rel.Title) {
		r := newRejection(ReasonDonorRejected, "a graft of this item already failed with %s", rel.Title)
		add(&r)
	}
	if len(t.Queue) > 0 {
		r := newRejection(ReasonDonorQueued, "a donor for this item is already downloading")
		add(&r)
	}
	add(sampleRejection(rel))
	rejections = append(rejections, blocklistAndHistoryRejections(t, rel)...)
	d := Decision{
		Release: rel, Parsed: parsed, Approved: len(rejections) == 0,
		TemporarilyRejected: len(rejections) > 0 && allTemporary(rejections),
		Rejections:          rejections, Score: score, Matched: matched,
	}
	if d.Approved {
		d.Rank = RankKey{Donor: true, DonorLineage: donorLineage(t.Donor, rel, parsed), SizeBytes: rel.SizeBytes}
	}
	return d
}

// donorLanguageRejection: a donor must carry every missing language and
// the anchor, and its title must say so -- by naming them, or (an anime
// score set's convention) by a dual-audio or dual/multi-language marker. A
// title that names no language assumes the original, which is exactly what
// a donor must be more than; the donor's probe at import is the real test.
func donorLanguageRejection(need []string, originalLanguage string, p quality.Profile, parsed *release.ParsedRelease, title string) *common.Rejection {
	var have []string
	if !parsed.LanguageUnknown {
		have = append(have, parsed.Languages...)
	}
	if dualAudioApplies(p, parsed, title, originalLanguage) {
		have = append(have, originalLanguage, "English")
	} else if strings.HasPrefix(p.ScoreSet, "anime-") && originalLanguage != "" && namesOnlyOriginalAndEnglish(parsed, originalLanguage) {
		if ok, err := multiLanguageRegex.MatchString(title); err == nil && ok {
			have = append(have, originalLanguage, "English")
		}
	}
	var missing []string
	for _, name := range need {
		if !containsFold(have, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	r := newRejection(ReasonDonorLanguage, "a donor must carry %v; the title names %v", need, have)
	return &r
}

// donorLineage is how close a donor is to the video (spec §6.1): the same
// source class scores 2, an edition that is not uncut or extended when the
// video is neither scores 1.
func donorLineage(d *Donor, rel common.ReleaseInfo, parsed *release.ParsedRelease) int {
	n := 0
	if d.Source != "" && rel.Quality.Source == d.Source {
		n += 2
	}
	long := func(e string) bool {
		e = strings.ToLower(e)
		return strings.Contains(e, "uncut") || strings.Contains(e, "extended")
	}
	if long(parsed.Edition) == long(d.Edition) {
		n++
	}
	return n
}

// donorNames are a donor's missing languages and its anchor as names,
// resolved once per Evaluate; a tag the table cannot name is left out (the
// profile that wanted it was already refused by quality.FromCRD).
func donorNames(ctx context.Context, d *Donor) []string {
	var out []string
	for _, l := range append(append([]string(nil), d.Languages...), d.Anchor) {
		if n := originalLanguageName(ctx, l); n != "" && !containsFold(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// namesOnlyOriginalAndEnglish is dualAudioApplies' guard for the DL and
// MULTi markers: German scene releases write DL for German and English,
// and a French MULTi names French, so a title naming any language besides
// the original and English is not believed.
func namesOnlyOriginalAndEnglish(parsed *release.ParsedRelease, originalLanguage string) bool {
	if parsed.LanguageUnknown {
		return true
	}
	for _, l := range parsed.Languages {
		if !strings.EqualFold(l, originalLanguage) && !strings.EqualFold(l, "English") {
			return false
		}
	}
	return true
}
