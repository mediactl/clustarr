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

package intake_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/app/intake"
)

// The inbox remembers why a candidate was refused, for the Search that
// sent a person's pick to say so: a settlement without a reason, and one
// that is no refusal, answer none.
func TestTheInboxRemembersWhyACandidateWasRefused(t *testing.T) {
	in := intake.NewInbox(4)
	in.SettleBecause("pick-1", intake.Refused, "QueueHigherPreference: a grab of equal or higher preference is already downloading")
	in.Settle("pick-2", intake.Refused)
	in.SettleBecause("pick-3", intake.Incorporated, "grabbed")

	o, ok := in.Settled("pick-1")
	assert.True(t, ok)
	assert.Equal(t, intake.Refused, o)
	assert.Equal(t, "QueueHigherPreference: a grab of equal or higher preference is already downloading", in.Refusal("pick-1"))
	assert.Empty(t, in.Refusal("pick-2"))
	assert.Empty(t, in.Refusal("pick-3"), "a candidate that was taken has no refusal")
	assert.Empty(t, in.Refusal("pick-4"), "nor one this leader never settled")
}
