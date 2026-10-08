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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/quality"
)

func TestVerdictMessage(t *testing.T) {
	// Every non-Upgrade verdict must render a non-empty, distinct message; a
	// blank or identical rejection string across verdicts would leave an
	// operator unable to tell two different rejections apart.
	verdicts := []quality.Verdict{
		quality.ExistingBetterQuality, quality.UpgradesNotAllowed, quality.ExistingBetterRevision,
		quality.QualityCutoffMet, quality.FormatScoreNotHigher, quality.FormatCutoffMet,
		quality.FormatIncrementTooSmall,
	}
	seen := map[string]bool{}
	for _, v := range verdicts {
		msg := verdictMessage(v)
		require.NotEmpty(t, msg)
		require.False(t, seen[msg], "verdict %d reused message %q", v, msg)
		seen[msg] = true
	}
}
