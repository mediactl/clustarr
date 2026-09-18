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
	"fmt"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

// Reason.Code values, ported 1:1 from the verified Radarr/Sonarr
// DownloadRejectionReason enum (docs/research/naming.md §A6; cross-checked
// against DecisionEngine/DownloadRejectionReason.cs in the vendored clone).
// "Existing*" corresponds to *arr's "Disk*" family (UpgradeDiskSpecification)
// -- renamed because Clustarr's Target.Current is the analogous concept, not
// a literal file on disk. Every value here is common.RejectionPermanent
// (Disagreement 6): the real Radarr/Sonarr source has every specification on
// the §8.2 checklist as RejectionType.Permanent -- the only Temporary
// specifications (minimum-age, blocked-indexer, RSS-sync-only) are all
// outside this task's checklist, so no Reason constant below uses
// common.RejectionTemporary even though Reason.Type can hold it.
var (
	ReasonUnableToParse               = Reason{"UnableToParse", common.RejectionPermanent}
	ReasonProtocolDisabled            = Reason{"ProtocolDisabled", common.RejectionPermanent}
	ReasonUnavailable                 = Reason{"Availability", common.RejectionPermanent}
	ReasonBelowMinimumSize            = Reason{"BelowMinimumSize", common.RejectionPermanent}
	ReasonAboveMaximumSize            = Reason{"AboveMaximumSize", common.RejectionPermanent}
	ReasonQualityNotWanted            = Reason{"QualityNotWanted", common.RejectionPermanent}
	ReasonCustomFormatMinimumScore    = Reason{"CustomFormatMinimumScore", common.RejectionPermanent}
	ReasonWantedLanguage              = Reason{"WantedLanguage", common.RejectionPermanent}
	ReasonSample                      = Reason{"Sample", common.RejectionPermanent}
	ReasonBlocklisted                 = Reason{"Blocklisted", common.RejectionPermanent}
	ReasonAlreadyImportedSameHash     = Reason{"AlreadyImportedSameHash", common.RejectionPermanent}
	ReasonAlreadyImportedSameName     = Reason{"AlreadyImportedSameName", common.RejectionPermanent}
	ReasonQueueHigherPreference       = Reason{"QueueHigherPreference", common.RejectionPermanent}
	ReasonExistingHigherPreference    = Reason{"ExistingHigherPreference", common.RejectionPermanent}
	ReasonUpgradesNotAllowed          = Reason{"UpgradesNotAllowed", common.RejectionPermanent}
	ReasonExistingHigherRevision      = Reason{"ExistingHigherRevision", common.RejectionPermanent}
	ReasonExistingCutoffMet           = Reason{"ExistingCutoffMet", common.RejectionPermanent}
	ReasonExistingFormatScore         = Reason{"ExistingFormatScore", common.RejectionPermanent}
	ReasonExistingFormatCutoffMet     = Reason{"ExistingFormatCutoffMet", common.RejectionPermanent}
	ReasonExistingFormatScoreIncrement = Reason{"ExistingFormatScoreIncrement", common.RejectionPermanent}
)

// verdictReasons maps every non-Upgrade quality.Verdict to the Reason
// upgradeRejection and queueRejection report it as (Disagreement 2: this
// table exists because pkg/decision calls quality.Profile.UpgradeDecision
// rather than reimplementing UpgradableSpecification).
var verdictReasons = map[quality.Verdict]Reason{
	quality.ExistingBetterQuality:   ReasonExistingHigherPreference,
	quality.UpgradesNotAllowed:      ReasonUpgradesNotAllowed,
	quality.ExistingBetterRevision:  ReasonExistingHigherRevision,
	quality.QualityCutoffMet:        ReasonExistingCutoffMet,
	quality.FormatScoreNotHigher:    ReasonExistingFormatScore,
	quality.FormatCutoffMet:         ReasonExistingFormatCutoffMet,
	quality.FormatIncrementTooSmall: ReasonExistingFormatScoreIncrement,
}

// VerdictReason exposes verdictReasons to tests in the decision_test
// package; production callers use it only through upgradeRejection/queueRejection.
func VerdictReason(v quality.Verdict) (Reason, bool) {
	r, ok := verdictReasons[v]
	return r, ok
}

// newRejection renders r as a common.Rejection: "<Code>: <formatted detail>",
// mirroring *arr's own Rejection.ToString() ("[Type] Message") closely enough
// that a reviewer can find the matching *arr specification by the Code alone.
func newRejection(r Reason, format string, args ...any) common.Rejection {
	return common.Rejection{
		Reason: r.Code + ": " + fmt.Sprintf(format, args...),
		Type:   r.Type,
	}
}
