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
	"math"
	"sort"
	"time"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// Rank stable-sorts a copy of ds best-first: LanguagesComplete first under a
// profile taking complete audio first (Audio.CompleteFirst), QualityIndex asc, Revision
// desc (only if PreferRevision), a whole season first, PreferredProtocolMatch
// desc, FormatScore desc, LanguagesComplete, CompareEpisodeCount (anime more
// episodes and any other series fewer), indexer priority asc (from
// o.IndexerPriority), indexer-flag score desc, seeders/age desc, then size
// (Disagreement 7) -- or, under a profile preferring the smallest, size asc
// right after LanguagesComplete. The season pack and the protocol rank ahead of the
// format score (owner, 2026-10-27: within a quality, a season pack beats
// single episodes and usenet beats torrent); Sonarr's comparer has them
// after it. Evaluate fills a RankKey for every decision whose title parsed,
// approved or not, so a caller ranks each band (BandOf) by the same
// comparator; Rank does not filter Rejected decisions itself.
func Rank(ds []Decision, o Options) []Decision {
	out := make([]Decision, len(ds))
	copy(out, ds)
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j], o) })
	return out
}

func less(a, b Decision, o Options) bool {
	if a.Rank.Donor && b.Rank.Donor {
		// An audio donor (spec §6.1): lineage close to the video, then the
		// smaller download; quality and format score mean nothing here.
		if a.Rank.DonorLineage != b.Rank.DonorLineage {
			return a.Rank.DonorLineage > b.Rank.DonorLineage
		}
		if a.Rank.SizeBytes != b.Rank.SizeBytes {
			return a.Rank.SizeBytes < b.Rank.SizeBytes
		}
	}
	// A profile taking complete audio first (owner, 2026-10-28: dual audio
	// is always taken when it exists) ranks it ahead of every other key.
	if a.Rank.CompleteFirst && b.Rank.CompleteFirst && a.Rank.LanguagesComplete != b.Rank.LanguagesComplete {
		return a.Rank.LanguagesComplete
	}
	if a.Rank.QualityIndex != b.Rank.QualityIndex {
		return a.Rank.QualityIndex < b.Rank.QualityIndex
	}
	if a.Rank.PreferRevision {
		if c := compareRevision(a.Rank.Revision, b.Rank.Revision); c != 0 {
			return c > 0 // higher revision (Real, then Version) wins
		}
	}
	if aw, bw := a.Rank.WholeSeason(), b.Rank.WholeSeason(); aw != bw {
		return aw
	}
	if a.Rank.PreferredProtocolMatch != b.Rank.PreferredProtocolMatch {
		return a.Rank.PreferredProtocolMatch
	}
	if a.Rank.FormatScore != b.Rank.FormatScore {
		return a.Rank.FormatScore > b.Rank.FormatScore
	}
	if a.Rank.LanguagesComplete != b.Rank.LanguagesComplete {
		return a.Rank.LanguagesComplete
	}
	// A profile preferring the smallest (QualityProfileSpec.PreferSmallest)
	// takes it ahead of the indexer, its flags and the seeders.
	if a.Rank.PreferSmallestSize && b.Rank.PreferSmallestSize && a.Rank.SizeBytes != b.Rank.SizeBytes {
		return a.Rank.SizeBytes < b.Rank.SizeBytes
	}
	if c := CompareEpisodeCount(a.Rank.WholeSeason(), a.Rank.EpisodeCount, b.Rank.WholeSeason(), b.Rank.EpisodeCount, a.Rank.Anime); c != 0 {
		return c < 0
	}
	if pa, pb := indexerPriority(o, a.Release.IndexerRef), indexerPriority(o, b.Release.IndexerRef); pa != pb {
		return pa < pb
	}
	if fa, fb := indexerFlagScore(a.Release.IndexerFlags), indexerFlagScore(b.Release.IndexerFlags); fa != fb {
		return fa > fb
	}
	if sa, sb := seedersOrAgeScore(a.Release), seedersOrAgeScore(b.Release); sa != sb {
		return sa > sb
	}
	if a.Rank.PreferLargestSize {
		return a.Rank.SizeBytes > b.Rank.SizeBytes
	}
	return a.Rank.SizeDeltaBucket < b.Rank.SizeDeltaBucket
}

// CompareEpisodeCount is Sonarr's DownloadDecisionComparer.CompareEpisodeCount
// (series search design §6.4): a whole-season release ranks first; then an
// anime series prefers more episodes and any other series fewer. <0 when a
// ranks first, 0 on a tie.
func CompareEpisodeCount(aWhole bool, aCount int, bWhole bool, bCount int, anime bool) int {
	if aWhole != bWhole {
		if aWhole {
			return -1
		}
		return 1
	}
	if aCount == bCount {
		return 0
	}
	if (aCount > bCount) == anime {
		return -1
	}
	return 1
}

// WholeSeason reports the season-pack sentinel EpisodeCount carries: a
// FullSeason release, or a season search's release covering every episode
// of the season (an anime batch).
func (k RankKey) WholeSeason() bool { return k.EpisodeCount == math.MaxInt32 }

// defaultIndexerPriority is indexers.md's documented default: "Priority int
// -- 1 (highest) … 50; used as the dedup tiebreaker ... default 25".
const defaultIndexerPriority = 25

func indexerPriority(o Options, ref string) int {
	if p, ok := o.IndexerPriority[ref]; ok {
		return p
	}
	return defaultIndexerPriority
}

// indexerFlagScore ports DownloadDecisionComparer.ScoreFlags's weights
// verbatim (verified against the vendored Radarr source, Disagreement 7):
// freeleech/doubleupload/internal +2, halfleech +1. neutralleech/exclusive/
// scene have no *arr equivalent and score 0.
func indexerFlagScore(flags []string) int {
	score := 0
	for _, f := range flags {
		switch f {
		case common.IndexerFlagFreeleech, common.IndexerFlagDoubleUpload, common.IndexerFlagInternal:
			score += 2
		case common.IndexerFlagHalfleech:
			score++
		}
	}
	return score
}

// seedersOrAgeScore ports ComparePeersIfTorrent/CompareAgeIfUsenet: torrent
// releases rank by log10(seeders); usenet releases rank by an age bucket
// (fresher wins), both verified against the vendored Radarr source.
func seedersOrAgeScore(rel common.ReleaseInfo) float64 {
	switch rel.Protocol {
	case common.ProtocolTorrent:
		if rel.Seeders == nil || *rel.Seeders <= 0 {
			return 0
		}
		return math.Round(math.Log10(float64(*rel.Seeders)))
	case common.ProtocolUsenet:
		// No reported publish date is not the same as a very old or a very
		// new one, so an unknown date scores neutral rather than winning or
		// losing the tiebreak outright.
		if rel.PublishedAt == nil {
			return 0
		}
		days, hours, _ := release.Age(rel.PublishedAt.Time, time.Now())
		switch {
		case hours < 1:
			return 1000
		case hours <= 24:
			return 100
		case days <= 7:
			return 10
		default:
			return math.Round(math.Log10(float64(days))) * -1
		}
	default:
		return 0
	}
}

// compareRevision orders by Real, then Version -- Revision.CompareTo,
// docs/research/quality.md §7.1. Version is floored at 1, as in
// pkg/quality's copy: see common.Revision.EffectiveVersion.
func compareRevision(x, y common.Revision) int {
	if x.Real != y.Real {
		if x.Real > y.Real {
			return 1
		}
		return -1
	}
	if xv, yv := x.EffectiveVersion(), y.EffectiveVersion(); xv != yv {
		if xv > yv {
			return 1
		}
		return -1
	}
	return 0
}
