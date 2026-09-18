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
	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/release"
)

// Fallback runtimes, docs/superpowers/specs/2026-09-18-clustarr-design.md
// §8.2: "size MB/min×runtime with 110/45-min fallbacks and summed episode
// runtimes for packs" (docs/research/quality.md §2.1 sources the two
// numbers from AcceptableSizeSpecification.cs / Sonarr's episode-runtime
// fallback).
const (
	fallbackMovieRuntimeMinutes   = 110
	fallbackEpisodeRuntimeMinutes = 45
)

// targetRuntimeMinutes resolves the runtime the size check uses. For a
// multi-episode release (FullSeason, or more than one parsed Episode) it
// sums Target.EpisodeRuntimes, applying the 45-minute fallback to each
// zero/missing entry before summing -- t.EpisodeRuntimes is read as "one
// entry per episode this specific release covers," so it takes the first
// min(len(parsed.Episodes), len(t.EpisodeRuntimes)) entries; a FullSeason
// release with no episode list at all falls back to exactly one 45-minute
// episode rather than reporting an unknown runtime (spec's fallback rule has
// no "unknown pack size" case). ok is false for a MediaKind this package has
// no size model for (music/book/audiobook/comic -- spec §9's "non-video
// pipelines... implemented after M6").
func targetRuntimeMinutes(t Target, parsed *release.ParsedRelease) (minutes int, ok bool) {
	switch t.Kind {
	case common.MediaKindMovie:
		m := t.RuntimeMinutes
		if m <= 0 {
			m = fallbackMovieRuntimeMinutes
		}
		return m, true

	case common.MediaKindEpisode, common.MediaKindSeries:
		multi := parsed.FullSeason || len(parsed.Episodes) > 1
		if !multi {
			m := 0
			if len(t.EpisodeRuntimes) > 0 {
				m = t.EpisodeRuntimes[0]
			}
			if m <= 0 {
				m = fallbackEpisodeRuntimeMinutes
			}
			return m, true
		}

		n := len(parsed.Episodes)
		if n == 0 || n > len(t.EpisodeRuntimes) {
			n = len(t.EpisodeRuntimes)
		}
		if n == 0 {
			return fallbackEpisodeRuntimeMinutes, true
		}
		total := 0
		for i := 0; i < n; i++ {
			m := t.EpisodeRuntimes[i]
			if m <= 0 {
				m = fallbackEpisodeRuntimeMinutes
			}
			total += m
		}
		return total, true

	default:
		return 0, false
	}
}

// sizeRejections is AcceptableSizeSpecification, ported: a release with
// unknown size (0) is never rejected; a quality absent from p.Sizes (e.g.
// SizeTable "none") is never checked; otherwise both bounds are enforced,
// with MaxMBPerMin 0 meaning unlimited (quality.SizeLimits' own convention).
func sizeRejections(t Target, p quality.Profile, parsed *release.ParsedRelease, rel common.ReleaseInfo) []common.Rejection {
	if rel.SizeBytes <= 0 {
		return nil
	}
	if _, hasLimits := p.Sizes[rel.Quality.Name]; !hasLimits {
		return nil
	}
	minutes, ok := targetRuntimeMinutes(t, parsed)
	if !ok {
		return nil
	}
	minBytes, maxBytes := quality.SizeLimits(p, rel.Quality, minutes)

	var out []common.Rejection
	if minBytes > 0 && rel.SizeBytes < minBytes {
		out = append(out, newRejection(ReasonBelowMinimumSize,
			"%d bytes is below the %s minimum of %d bytes at %d minutes", rel.SizeBytes, rel.Quality.Name, minBytes, minutes))
	}
	if maxBytes > 0 && rel.SizeBytes > maxBytes {
		out = append(out, newRejection(ReasonAboveMaximumSize,
			"%d bytes is above the %s maximum of %d bytes at %d minutes", rel.SizeBytes, rel.Quality.Name, maxBytes, minutes))
	}
	return out
}
