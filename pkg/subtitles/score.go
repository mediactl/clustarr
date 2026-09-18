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

package subtitles

import (
	"strings"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

const (
	MatchHash             = "hash"
	MatchSeries           = "series"
	MatchTitle            = "title"
	MatchYear             = "year"
	MatchSeason           = "season"
	MatchEpisode          = "episode"
	MatchSource           = "source"
	MatchReleaseGroup     = "release_group"
	MatchAudioCodec       = "audio_codec"
	MatchResolution       = "resolution"
	MatchVideoCodec       = "video_codec"
	MatchHearingImpaired  = "hearing_impaired"
	MatchStreamingService = "streaming_service"
	MatchEdition          = "edition"
)

const (
	MaxEpisodeScore = 360
	MaxMovieScore   = 180
)

// MaxScore is spec §7's exact symbol.
var MaxScore = map[common.MediaKind]int{common.MediaKindEpisode: MaxEpisodeScore, common.MediaKindMovie: MaxMovieScore}

// episodeWeights/movieWeights are research note §5's DEFAULT_SCORES table,
// verbatim.
var episodeWeights = map[string]int{
	MatchHash: 359, MatchSeries: 160, MatchYear: 90, MatchSeason: 30, MatchEpisode: 30,
	MatchSource: 25, MatchReleaseGroup: 20, MatchAudioCodec: 1, MatchResolution: 1,
	MatchVideoCodec: 1, MatchHearingImpaired: 1, MatchStreamingService: 1,
}

var movieWeights = map[string]int{
	MatchHash: 179, MatchTitle: 60, MatchYear: 40, MatchSource: 30, MatchEdition: 30,
	MatchReleaseGroup: 15, MatchAudioCodec: 1, MatchResolution: 1, MatchVideoCodec: 1,
	MatchHearingImpaired: 1, MatchStreamingService: 1,
}

func weightsFor(kind common.MediaKind) map[string]int {
	if kind == common.MediaKindMovie {
		return movieWeights
	}
	return episodeWeights // episode is the only other subtitle-eligible kind (design spec §4.6)
}

// Score is spec §7's exact signature: sums per-kind weights over an
// already-resolved matches map (research note §5 item 3).
func Score(kind common.MediaKind, matches map[string]bool) (score, without int) {
	w := weightsFor(kind)
	for m, ok := range matches {
		if ok {
			score += w[m]
		}
	}
	without = score
	if matches[MatchHash] {
		without = score - w[MatchHash]
	}
	return score, without
}

// MinScore converts a profile's MinScorePercent into an absolute threshold:
// MaxScore[kind] * pct / 100 (integer math — no float, per CLAUDE.md).
func MinScore(kind common.MediaKind, pct int) int {
	return MaxScore[kind] * pct / 100
}

// CandidateMatches implements compute_score's hash-corroboration and
// hearing-impaired-bonus steps (research note §5 items 1-2). wantHIMatch is
// the candidate's own HI flag compared against the profile's HI preference
// for this language; nil means no preference is set (Bazarr's "prefer":
// either is fine, no bonus either way).
func CandidateMatches(kind common.MediaKind, hashVerifiable, isSpecial bool, wantHIMatch *bool, raw map[string]bool) map[string]bool {
	out := make(map[string]bool, len(raw))
	for k, v := range raw {
		out[k] = v
	}

	if out[MatchHash] {
		corroborated := !hashVerifiable // an unverifiable provider is trusted outright
		if hashVerifiable {
			if kind == common.MediaKindMovie {
				corroborated = out[MatchVideoCodec] && out[MatchSource]
			} else {
				corroborated = out[MatchSeries] && out[MatchSource] && (isSpecial || (out[MatchSeason] && out[MatchEpisode]))
			}
		}
		if corroborated {
			out = map[string]bool{MatchHash: true}
		} else {
			delete(out, MatchHash)
		}
	}

	if wantHIMatch != nil && *wantHIMatch {
		out[MatchHearingImpaired] = true
	}
	return out
}

// GuessMatches derives the Match keys a candidate's raw release_info string
// corroborates against the query's target release. Per the controller
// amendment (wave 2, pkg/release has landed), it parses releaseInfo with
// release.Parse — kind-pinned to kind, the same kind the caller's Query
// carries — and compares the result field-for-field with target: Group
// (case-insensitive), Quality.Source, Quality.Resolution, Hints.Codec (any
// element in common), Hints.Audio (any in common), Hints.Streaming (any in
// common), and Edition (case-insensitive). A nil target or a parse error
// yields no release-derived matches — never an error, since a candidate
// whose release_info happens to be unparseable is simply scored without
// release corroboration rather than rejected outright.
func GuessMatches(kind common.MediaKind, target *release.ParsedRelease, releaseInfo string) map[string]bool {
	out := map[string]bool{}
	if target == nil {
		return out
	}
	cand, err := release.Parse(releaseInfo, release.Options{Kind: kind})
	if err != nil {
		return out
	}

	if target.Group != "" && strings.EqualFold(target.Group, cand.Group) {
		out[MatchReleaseGroup] = true
	}
	if target.Quality.Source != "" && target.Quality.Source == cand.Quality.Source {
		out[MatchSource] = true
	}
	if target.Quality.Resolution != 0 && target.Quality.Resolution == cand.Quality.Resolution {
		out[MatchResolution] = true
	}
	if anyCommonFold(target.Hints.Codec, cand.Hints.Codec) {
		out[MatchVideoCodec] = true
	}
	if anyCommonFold(target.Hints.Audio, cand.Hints.Audio) {
		out[MatchAudioCodec] = true
	}
	if anyCommonFold(target.Hints.Streaming, cand.Hints.Streaming) {
		out[MatchStreamingService] = true
	}
	if target.Edition != "" && strings.EqualFold(target.Edition, cand.Edition) {
		out[MatchEdition] = true
	}
	return out
}

// anyCommonFold reports whether a and b share at least one element, compared
// case-insensitively.
func anyCommonFold(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if strings.EqualFold(x, y) {
				return true
			}
		}
	}
	return false
}
