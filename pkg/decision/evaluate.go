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
	"math"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// Evaluate runs the §8.2 checklist against every release in rels for one
// Target, in the order: identity (is the release for this item at all --
// identity.go), protocol enabled, availability (skipped when o.UserInvoked),
// size, quality-in-profile + MinFormatScore, language, sample, blocklist +
// already-imported, queue preference, the transcoded-final check (a
// transcoded current file is never upgraded automatically; skipped when
// o.UserInvoked), then the UpgradableSpecification table via
// p.UpgradeDecision. Every applicable
// check runs (nothing short-circuits except an unparseable title), so a
// release can carry more than one Rejection -- matching the real
// DownloadDecision's Rejections list, which Approved/TemporarilyRejected
// are then derived from exactly as Radarr's DownloadDecision does
// (Disagreement 6).
func Evaluate(ctx context.Context, t Target, p quality.Profile, cat *catalogue.Catalogue, rels []common.ReleaseInfo, o Options) []Decision {
	// Resolved once per call, not once per release: the lookup is the same
	// for every candidate, and an unresolvable tag must warn once about the
	// item rather than once about every release of it.
	lang := originalLanguageName(ctx, t.OriginalLanguageTag)
	// Likewise the item's identity keys (title keys, scene mapping): the same
	// for every candidate.
	idx := newIdentityIndex(t.Kind, t.Identity)
	out := make([]Decision, 0, len(rels))
	for _, rel := range rels {
		out = append(out, evaluateOne(ctx, t, lang, idx, p, cat, rel, o))
	}
	return out
}

// evaluateOne takes originalLanguage -- the item's original language already
// resolved into the English display-name vocabulary, "" when unknown --
// rather than re-deriving it from t, so there is exactly one conversion per
// Evaluate and both consumers below are fed from it. idx is
// newIdentityIndex(t.Kind, t.Identity), computed once per Evaluate for the
// same reason.
func evaluateOne(ctx context.Context, t Target, originalLanguage string, idx identityIndex, p quality.Profile, cat *catalogue.Catalogue, rel common.ReleaseInfo, o Options) Decision {
	parsed, err := release.Parse(rel.Title, release.Options{Kind: t.Kind})
	if err != nil {
		logging.FromContext(ctx).Debug("decision: release title did not parse", "title", rel.Title, "err", err)
		return Decision{
			Release:    rel,
			Rejections: []common.Rejection{newRejection(ReasonUnableToParse, "%v", err)},
		}
	}
	// A title that names no language takes the item's original language
	// before anything reads the languages -- custom-format scoring, the
	// language check, and the ReleaseInfo this Decision carries -- which
	// is the order Radarr's AggregateLanguages runs in (see
	// release.ParsedRelease.LanguagesFor).
	parsed.Languages = parsed.LanguagesFor(originalLanguage)
	parsed.ApplyTo(&rel)

	// ReleaseTitle is the indexer's full release name: ReleaseTitle custom
	// format conditions (repack, HDR, codecs, streaming services) read it, not
	// the parsed item title. A release has no file yet, so no Filename.
	ic := catalogue.ItemContext{
		OriginalLanguageName: originalLanguage, IndexerFlags: rel.IndexerFlags, ReleaseType: parsed.ReleaseType,
		ReleaseTitle: rel.Title,
	}
	score, matched := p.Score(ctx, cat, parsed, ic)
	rel.FormatScore = int32(score)
	rel.MatchedFormats = capMatchedFormats(matched)

	var rejections []common.Rejection
	add := func(r *common.Rejection) {
		if r != nil {
			rejections = append(rejections, *r)
		}
	}

	add(identityRejection(t, idx, parsed, rel))
	add(protocolRejection(rel, o))
	add(availabilityRejection(t, o))
	rejections = append(rejections, sizeRejections(t, p, parsed, rel)...)
	rejections = append(rejections, qualityRejections(p, rel, score)...)
	add(languageRejection(originalLanguage, p, parsed))
	add(sampleRejection(rel))
	rejections = append(rejections, blocklistAndHistoryRejections(t, rel)...)

	candidate := quality.Candidate{Quality: rel.Quality, Revision: rel.Revision, FormatScore: score}
	add(queueRejection(p, t, candidate))
	add(transcodedRejection(t, o))
	add(upgradeRejection(p, t, candidate))

	d := Decision{
		Release:             rel,
		Parsed:              parsed,
		Approved:            len(rejections) == 0,
		TemporarilyRejected: len(rejections) > 0 && allTemporary(rejections),
		Rejections:          rejections,
		Score:               score,
		Matched:             matched,
	}
	if d.Approved {
		d.Rank = buildRankKey(p, o, t, parsed, rel, score)
	}
	return d
}

// maxMatchedFormats is ReleaseInfo.MatchedFormats' +kubebuilder:validation:MaxItems.
// Decision.Release is what lands in Search.status.results and
// Download.spec.release, and an apply carrying a longer list is rejected
// whole -- one over-matching release would fail the entire Search. Only
// the copy on the Release is capped; Decision.Matched keeps every match.
const maxMatchedFormats = 200

func capMatchedFormats(matched []string) []string {
	if len(matched) > maxMatchedFormats {
		return matched[:maxMatchedFormats]
	}
	return matched
}

func allTemporary(rejections []common.Rejection) bool {
	for _, r := range rejections {
		if r.Type != common.RejectionTemporary {
			return false
		}
	}
	return true
}

// buildRankKey computes every field of RankKey that Evaluate alone has
// enough context to fill in (Profile and Target); Rank's own comparator
// chain supplies the remaining keys (indexer priority/flags, seeders/age)
// from Options and the Decision's Release at sort time.
func buildRankKey(p quality.Profile, o Options, t Target, parsed *release.ParsedRelease, rel common.ReleaseInfo, score int) RankKey {
	idx, _ := p.Index(rel.Quality)

	preferredProtocol := o.PreferredProtocol
	if preferredProtocol == "" {
		preferredProtocol = p.PreferredProtocol
	}
	protocolMatch := preferredProtocol == "any" || string(rel.Protocol) == preferredProtocol

	episodeCount := 1
	switch {
	case parsed.FullSeason:
		episodeCount = math.MaxInt32
	case len(parsed.Episodes) > 1:
		episodeCount = len(parsed.Episodes)
	}

	sl := p.Sizes[rel.Quality.Name]
	key := RankKey{
		QualityIndex:           idx,
		PreferRevision:         p.ProperPolicy != "doNotPrefer",
		Revision:               rel.Revision,
		FormatScore:            score,
		PreferredProtocolMatch: protocolMatch,
		EpisodeCount:           episodeCount,
	}
	if preferLargest(sl) {
		key.PreferLargestSize = true
		key.SizeBytes = rel.SizeBytes
		return key
	}
	if minutes, ok := targetRuntimeMinutes(t, parsed); ok {
		prefBytes := int64(sl.PrefMBPerMin * float64(minutes) * 1024 * 1024)
		key.SizeDeltaBucket = roundTo200MiB(abs64(rel.SizeBytes - prefBytes))
	}
	return key
}

// preferLargestRatio is how close PrefMBPerMin must sit to MaxMBPerMin before
// the pair is read as TRaSH's "no effective ceiling, take the biggest" sentinel
// rather than a real target. The real tables sit at 0.9995 (movies and anime,
// 1999/2000) and 0.9950 (series, 995/1000), while an ordinary profile's
// preferred size is far below its max, so 0.99 separates them with room to
// spare. An absolute tolerance does NOT work here: `Pref >= Max-1` matches
// 1999/2000 but misses 995/1000.
//
// Confirmed against the upstream tables (ruling R-12, TRaSH-Guides master
// docs/json/{radarr,sonarr}/quality-size/*.json, 2026-09-23): every entry of
// radarr movie.json, radarr anime.json and sqp-uhd.json is 1999/2000, and
// every entry of sonarr series.json and anime.json is 995/1000, so the
// lowest shipped ratio is 0.995 -- TestPreferLargestRatioAgainstEveryShippedEntry
// pins all of them. The one upstream table that lands on both sides of 0.99
// is radarr sqp-streaming.json (preferred = max - 1 on real caps: 84.7/85.7 =
// 0.9883 up to 221.2/222.2 = 0.9955). Clustarr does not ship it (sizeTable
// is movie, series, anime or none), and it would not matter if a profile
// override copied it: with preferred a hair under max and the runtime
// known, the closest-to-preferred branch and the largest branch order every
// release below preferred identically (smaller distance to preferred is
// larger size), and differ only within the last 1 MB/min under the cap.
// (With the runtime unknown the closest-to-preferred branch ranks no size at
// all; that difference is reachable only through such an override.)
const preferLargestRatio = 0.99

// preferLargest reports whether q's preferred size is TRaSH's own "biggest"
// sentinel rather than a real target: the shipped tables set PrefMBPerMin
// just below MaxMBPerMin (1999/2000 movies and anime, 995/1000 series) to
// mean exactly that (docs/research/quality.md §2.2: `"2000" is the UI value
// for unlimited, 1999 preferred = "biggest"`). A profile's SizeLimits
// override that sets a materially lower preferred value is a real target
// and takes the closest-to-preferred branch instead.
func preferLargest(sl quality.SizeLimit) bool {
	if sl.MaxMBPerMin == 0 {
		return true // unlimited
	}
	return sl.PrefMBPerMin >= sl.MaxMBPerMin*preferLargestRatio
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

const sizeBucket = 200 * 1024 * 1024 // 200 MiB, DownloadDecisionComparer.CompareSize's own bucket

func roundTo200MiB(n int64) int64 {
	return (n + sizeBucket/2) / sizeBucket * sizeBucket
}
