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

package quality

import common "github.com/mediactl/clustarr/api/common/v1alpha1"

// Candidate is the quality-model input to UpgradeDecision: current or
// candidate release quality, revision and total matched custom-format score.
//
// UpgradeDecision is a profile-scoped subset of spec's pkg/decision.Upgradable
// (scope decision 5 in the task doc): the full multi-candidate ranking
// machinery (availability, blocklist, protocol preference, queue) belongs to
// a later phase's pkg/decision, which will very likely call through to this
// function rather than reimplement it.
type Candidate struct {
	Quality     common.Quality
	Revision    common.Revision
	FormatScore int
}

// Verdict is the outcome of UpgradeDecision, matching
// UpgradableSpecification.IsUpgradable's return states verbatim
// (docs/research/quality.md §6.1). Upgrade is the only verdict meaning
// "grab the candidate"; every other value names the reason it did not win.
type Verdict uint8

// Verdict values, in the order docs/research/quality.md §6.1's pseudocode
// returns them.
const (
	Upgrade Verdict = iota
	ExistingBetterQuality
	UpgradesNotAllowed
	ExistingBetterRevision
	QualityCutoffMet
	FormatScoreNotHigher
	FormatCutoffMet
	FormatIncrementTooSmall
)

// UpgradeDecision ports UpgradableSpecification.IsUpgradable
// (docs/research/quality.md §6.1) for one current/candidate pair: quality
// rank first (by p.Index, group members tie), then revision (Real then
// Version) unless p.ProperPolicy is "doNotPrefer", then custom-format score.
// It assumes both Qualities are Allowed by p -- filtering disallowed
// qualities is the search/decision layer's job, not this function's; a
// candidate Quality unknown to p is treated as maximally bad (never an
// upgrade) rather than panicking.
func (p Profile) UpgradeDecision(current, candidate Candidate) Verdict {
	curIdx, curOK := p.Index(current.Quality)
	newIdx, newOK := p.Index(candidate.Quality)
	if !newOK {
		return ExistingBetterQuality
	}
	if !curOK {
		curIdx = len(p.Tiers)
	}
	qualityCompare := curIdx - newIdx // > 0: candidate is better (lower index)

	if qualityCompare > 0 && !p.CutoffMet(current.Quality) {
		return Upgrade
	}
	if qualityCompare < 0 {
		return ExistingBetterQuality
	}

	// A revision-only (proper/repack) upgrade only applies to the identical
	// quality -- "don't upgrade to a proper for a WEBRip from a WEBDL or vice
	// versa" (docs/research/quality.md §6.1's IsRevisionUpgrade note) -- so
	// this is gated on quality identity, not merely on qualityCompare == 0
	// (which only proves the two tie in tier, e.g. WEBRip-1080p and
	// WEBDL-1080p in one "WEB 1080p" tier).
	sameDef := qualityCompare == 0 &&
		current.Quality.Source == candidate.Quality.Source &&
		current.Quality.Resolution == candidate.Quality.Resolution &&
		current.Quality.Modifier == candidate.Quality.Modifier
	preferPropers := p.ProperPolicy != "doNotPrefer"
	revCompare := compareRevision(candidate.Revision, current.Revision)

	if sameDef && preferPropers && revCompare > 0 {
		return Upgrade
	}
	if !p.UpgradeAllowed {
		return UpgradesNotAllowed
	}
	if sameDef && preferPropers && revCompare < 0 {
		return ExistingBetterRevision
	}
	if qualityCompare > 0 {
		return QualityCutoffMet
	}

	if candidate.FormatScore <= current.FormatScore {
		return FormatScoreNotHigher
	}
	if current.FormatScore >= p.CutoffFormatScore {
		return FormatCutoffMet
	}
	if candidate.FormatScore < current.FormatScore+p.MinUpgradeFormatScore {
		return FormatIncrementTooSmall
	}
	return Upgrade
}

// compareRevision orders by Real first, then Version, matching
// Revision.CompareTo (docs/research/quality.md §6.1): positive means a is
// the better (more-upgraded) revision.
func compareRevision(a, b common.Revision) int {
	if a.Real != b.Real {
		return int(a.Real - b.Real)
	}
	return int(a.Version - b.Version)
}
