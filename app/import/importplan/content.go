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
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
)

// The movie runtime rule's bounds, calibrated on the owner's library
// (2026-10-16). Of its 839 movie files, every one under ShortPercent of
// its movie's runtime is a fragment -- five Up films and No Direction Home
// are one broadcast part of the film, Ennio an abridgement -- while the
// shortest real cut runs 71% (Enter the Void). Of the 92 imported from a
// download, six had a release title that does not name the movie -- a
// German title, "ThelmaandLouise", a REPACK the parser misreads -- and five
// of those run within 1% of it; the sixth, 24% long, is No Escape imported
// as The NeverEnding Story III. A title that names the movie is trusted at
// any length: director's cuts and extended editions run up to 82% long.
const (
	ShortPercent               = 65
	WrongTitleTolerancePercent = 15
	WrongTitleToleranceMinutes = 10
	// JoinedShortPercent is the least of the movie's runtime a movie
	// joined from its parts runs (joinedRejection): the whole film, a cut
	// shorter than the metadata's allowed.
	JoinedShortPercent = 85
)

// movieContentRejection refuses a movie file whose own streams say it is
// not the movie, the release's fault each time (2026-10-16):
//   - a Dolby Vision file (decision.ReasonDolbyVision's rule, read from the
//     probe for a release whose title did not say so);
//   - one that runs under ShortPercent of the movie: a part of the film;
//   - one whose release title does not name the movie and whose runtime is
//     more than WrongTitleTolerancePercent (and WrongTitleToleranceMinutes)
//     off it: an indexer answered the movie's id with another film, which
//     the id query's vouch took on its year alone.
//
// Each fails open on what it does not know: no probe, no duration, no
// metadata runtime.
func movieContentRejection(rel string, probe *schema.ProbeSummary, releaseTitle string, id decision.Identity, runtimeMinutes int) Rejection {
	if probe == nil {
		return Rejection{}
	}
	if decision.IsDolbyVision(probe.HDR) {
		return releaseFaultRejection("%s: the file is Dolby Vision (%s), which is never taken", rel, probe.HDR)
	}
	if runtimeMinutes <= 0 || probe.DurationSeconds <= 0 {
		return Rejection{}
	}
	runs := int(probe.DurationSeconds / 60)
	if runs*100 < runtimeMinutes*ShortPercent {
		return releaseFaultRejection("%s: runs %d minutes, under %d%% of the movie's %d: a part of it, not the film",
			rel, runs, ShortPercent, runtimeMinutes)
	}
	off := runs - runtimeMinutes
	if off < 0 {
		off = -off
	}
	if off*100 > runtimeMinutes*WrongTitleTolerancePercent && off > WrongTitleToleranceMinutes &&
		releaseTitle != "" && !decision.TitleNamesMovie(id, releaseTitle) {
		return releaseFaultRejection("%s: runs %d minutes to the movie's %d, and the release %q does not name it: another film",
			rel, runs, runtimeMinutes, releaseTitle)
	}
	return Rejection{}
}

// baselineLoss is decision.BaselineLoss between mf and the incoming file c
// would replace it with, "" when it keeps mf's baseline or either side's
// streams are unknown.
func baselineLoss(mf *catalogv1alpha1.MediaFile, c candidate) string {
	mi, probe := mf.Status.MediaInfo, c.f.Probe
	if mi == nil || probe == nil {
		return ""
	}
	var res int32
	if q := c.f.Frozen.Quality; q != nil {
		res = q.Resolution
	}
	return decision.BaselineLoss(mi.VideoCodec, mi.Hdr, mf.Spec.Quality.Resolution, probe.VideoCodec, probe.HDR, res)
}

// currentScore is mf's format score as p scores its frozen formats today
// (quality.Profile.ScoreFormats), so a score the profile has changed since
// -- x265 (HD)'s, 2026-10-16 -- is the one an import must beat; the frozen
// score when mf recorded no formats.
func currentScore(p quality.Profile, mf *catalogv1alpha1.MediaFile) int {
	if len(mf.Spec.MatchedFormats) == 0 {
		return int(mf.Spec.FormatScore)
	}
	return p.ScoreFormats(mf.Spec.MatchedFormats)
}
