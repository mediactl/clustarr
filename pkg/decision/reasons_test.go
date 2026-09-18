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

package decision_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
)

func TestEveryReasonIsPermanent(t *testing.T) {
	// Disagreement 6: every real *arr specification on the §8.2 checklist is
	// RejectionType.Permanent; this package emits no Temporary reason.
	for _, r := range []decision.Reason{
		decision.ReasonUnableToParse, decision.ReasonProtocolDisabled, decision.ReasonUnavailable,
		decision.ReasonBelowMinimumSize, decision.ReasonAboveMaximumSize, decision.ReasonQualityNotWanted,
		decision.ReasonCustomFormatMinimumScore, decision.ReasonWantedLanguage, decision.ReasonSample,
		decision.ReasonBlocklisted, decision.ReasonAlreadyImportedSameHash, decision.ReasonAlreadyImportedSameName,
		decision.ReasonQueueHigherPreference, decision.ReasonExistingHigherPreference, decision.ReasonUpgradesNotAllowed,
		decision.ReasonExistingHigherRevision, decision.ReasonExistingCutoffMet, decision.ReasonExistingFormatScore,
		decision.ReasonExistingFormatCutoffMet, decision.ReasonExistingFormatScoreIncrement,
	} {
		require.Equal(t, common.RejectionPermanent, r.Type, "reason %s", r.Code)
	}
}

func TestVerdictReasonTableIsExhaustive(t *testing.T) {
	// Every Verdict quality.UpgradeDecision can return, other than Upgrade
	// itself (which means "approve, don't reject"), must have a mapped Reason.
	for _, v := range []quality.Verdict{
		quality.ExistingBetterQuality, quality.UpgradesNotAllowed, quality.ExistingBetterRevision,
		quality.QualityCutoffMet, quality.FormatScoreNotHigher, quality.FormatCutoffMet, quality.FormatIncrementTooSmall,
	} {
		r, ok := decision.VerdictReason(v)
		require.True(t, ok, "verdict %v has no mapped Reason", v)
		require.NotEmpty(t, r.Code)
	}
	_, ok := decision.VerdictReason(quality.Upgrade)
	require.False(t, ok, "Upgrade must never map to a Reason -- it means approve")
}
