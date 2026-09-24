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

package wantedcron

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestBackoff(t *testing.T) {
	cases := []struct {
		count int32
		want  time.Duration
	}{
		{0, 6 * time.Hour},  // no attempt recorded yet: the minimum gap
		{1, 6 * time.Hour},  // n = Count-1 = 0, so 6h * 2^0
		{2, 12 * time.Hour}, //
		{3, 24 * time.Hour}, //
		{4, 48 * time.Hour}, //
		{5, 96 * time.Hour}, //
		{6, 7 * 24 * time.Hour},
		{10, 7 * 24 * time.Hour},         // capped
		{1 << 20, 7 * 24 * time.Hour},    // the shift is capped before it overflows
		{2147483647, 7 * 24 * time.Hour}, // math.MaxInt32
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, Backoff(commonv1.Attempts{Count: c.count}), "Backoff(count=%d)", c.count)
	}
}

func TestNextEligible(t *testing.T) {
	assert.True(t, NextEligible(commonv1.Attempts{}).IsZero(),
		"an item with no recorded attempt is eligible now, not after a backoff window")

	latest := metav1.NewTime(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC))
	assert.Equal(t, latest.Add(6*time.Hour), NextEligible(commonv1.Attempts{Latest: &latest, Count: 1}))
	assert.Equal(t, latest.Add(48*time.Hour), NextEligible(commonv1.Attempts{Latest: &latest, Count: 4}))

	// Count without Latest is still eligible: the gap is measured from the
	// last attempt, and there is no last attempt to measure from.
	assert.True(t, NextEligible(commonv1.Attempts{Count: 9}).IsZero())
}

func TestEligible(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	latest := metav1.NewTime(now.Add(-7 * time.Hour))
	recent := metav1.NewTime(now.Add(-time.Hour))

	assert.True(t, Eligible(commonv1.Attempts{}, now), "never searched")
	assert.True(t, Eligible(commonv1.Attempts{Latest: &latest, Count: 1}, now), "7h ago, 6h gap")
	assert.False(t, Eligible(commonv1.Attempts{Latest: &recent, Count: 1}, now), "1h ago, 6h gap")
	assert.False(t, Eligible(commonv1.Attempts{Latest: &latest, Count: 3}, now), "7h ago, 24h gap")

	// The boundary is inclusive: exactly one gap later is eligible.
	exact := metav1.NewTime(now.Add(-6 * time.Hour))
	assert.True(t, Eligible(commonv1.Attempts{Latest: &exact, Count: 1}, now))
}
