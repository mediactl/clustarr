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

package movie_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/movie"
)

func mt(y int, m time.Month, d int) *metav1.Time {
	t := metav1.NewTime(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
	return &t
}

func TestAvailability(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		min       catalogv1alpha1.MinimumAvailability
		meta      *catalogv1alpha1.MovieMetadata
		delayDays int32
		wantAvail bool
		wantAt    time.Time
	}{
		{"tba always available, nil metadata", catalogv1alpha1.MinimumAvailabilityTBA, nil, 0, true, time.Time{}},
		{"announced always available", catalogv1alpha1.MinimumAvailabilityAnnounced, &catalogv1alpha1.MovieMetadata{}, 30, true, time.Time{}},
		{"inCinemas known, past date, no delay", catalogv1alpha1.MinimumAvailabilityInCinemas,
			&catalogv1alpha1.MovieMetadata{InCinemas: mt(2026, 8, 1)}, 0, true, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
		{"inCinemas known, future date", catalogv1alpha1.MinimumAvailabilityInCinemas,
			&catalogv1alpha1.MovieMetadata{InCinemas: mt(2026, 10, 1)}, 0, false, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{"inCinemas known, delay pushes it past now", catalogv1alpha1.MinimumAvailabilityInCinemas,
			&catalogv1alpha1.MovieMetadata{InCinemas: mt(2026, 9, 10)}, 14, false, time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)},
		{"inCinemas requested but unknown falls through to released-style logic",
			catalogv1alpha1.MinimumAvailabilityInCinemas,
			&catalogv1alpha1.MovieMetadata{PhysicalRelease: mt(2026, 9, 1)}, 0, true, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		{"released, both dates known, picks the earlier", catalogv1alpha1.MinimumAvailabilityReleased,
			&catalogv1alpha1.MovieMetadata{PhysicalRelease: mt(2026, 10, 1), DigitalRelease: mt(2026, 9, 1)}, 0, true, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		{"released, only digital known", catalogv1alpha1.MinimumAvailabilityReleased,
			&catalogv1alpha1.MovieMetadata{DigitalRelease: mt(2026, 9, 20)}, 0, false, time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)},
		{"released, only inCinemas known, +90 days", catalogv1alpha1.MinimumAvailabilityReleased,
			&catalogv1alpha1.MovieMetadata{InCinemas: mt(2026, 1, 1)}, 0, true, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{"released, nothing known, never available", catalogv1alpha1.MinimumAvailabilityReleased,
			&catalogv1alpha1.MovieMetadata{}, 0, false, time.Time{}},
		{"released, nil metadata, never available", catalogv1alpha1.MinimumAvailabilityReleased, nil, 0, false, time.Time{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			avail, at := movie.Availability(c.min, c.meta, c.delayDays, now)
			assert.Equal(t, c.wantAvail, avail)
			assert.True(t, c.wantAt.Equal(at), "availableAt = %v, want %v", at, c.wantAt)
		})
	}
}
