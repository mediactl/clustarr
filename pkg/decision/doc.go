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

// Package decision is the pure release-decision engine every search, RSS and
// grab path runs (spec §7, §8.2, §9). Evaluate runs the full §8.2 checklist
// (protocol, availability, size, quality, MinFormatScore, language, sample,
// blocklist, already-imported, queue preference, and the
// UpgradableSpecification table via pkg/quality.Profile.UpgradeDecision)
// against every candidate release for one Target, and Rank orders the
// approved ones. Every rejection is a typed Reason, not a bare string
// (reasons.go), and every one this package emits is common.RejectionPermanent
// -- see the package's task plan for why. This package has no Kubernetes
// client and does no I/O or network access; a controller calls Evaluate/Rank
// and stores the result (CLAUDE.md's "pure functions where the logic is
// tricky").
package decision
