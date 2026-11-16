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

package lookuphold_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mediactl/clustarr/app/catalog/controller/lookuphold"
)

// A hold this process already keeps counts from its own first failure: the
// condition's older transition (an Artist False for three hours while its
// metadata was pending, then failing its listing) seeds only a first
// failure, as after a restart, never a second one (review of 1ac67d89,
// 2026-11-16).
func TestASecondFailureCountsFromTheFirstNotTheConditionsTransition(t *testing.T) {
	var h lookuphold.Holds[string]
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	pending := []metav1.Condition{{Type: "AlbumsSynced", Status: metav1.ConditionFalse, Reason: "MetadataPending", LastTransitionTime: metav1.NewTime(now.Add(-3 * time.Hour))}}
	err := errors.New("rate: Wait(n=1) would exceed context deadline")

	first := h.Failed("cher", now, lookuphold.Since(pending, "AlbumsSynced", now, "MetadataPending"), err)
	assert.LessOrEqual(t, first.Until.Sub(now), lookuphold.Min, "a pending wait is no failed lookup")

	failing := []metav1.Condition{{Type: "AlbumsSynced", Status: metav1.ConditionFalse, Reason: "RPCError", LastTransitionTime: metav1.NewTime(now.Add(-3 * time.Hour))}}
	at := first.Until
	second := h.Failed("cher", at, lookuphold.Since(failing, "AlbumsSynced", at, "MetadataPending"), err)
	assert.LessOrEqual(t, second.Until.Sub(at), lookuphold.Min, "the second failure counts from the first, a minute ago")

	var restarted lookuphold.Holds[string]
	seeded := restarted.Failed("cher", at, lookuphold.Since(failing, "AlbumsSynced", at, "MetadataPending"), err)
	assert.GreaterOrEqual(t, seeded.Until.Sub(at), lookuphold.Max/2, "a restarted process seeds from the condition")
}
