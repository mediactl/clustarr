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

package natsbus

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The guard trips on many orphans that are also a large share of the bucket:
// a few leaked Puts never trip it, nor does a small bucket.
func TestOrphanGuardTripsOnManyOrphansThatAreMuchOfTheBucket(t *testing.T) {
	for _, tc := range []struct {
		name              string
		orphans, subjects int
		want              bool
	}{
		{"no orphans", 0, 10000, false},
		{"a few leaked Puts", 5, 10000, false},
		{"99 of 100: under the floor", 99, 100, false},
		{"100 of 100", 100, 100, true},
		{"100 of 400: a quarter exactly", 100, 400, false},
		{"101 of 400", 101, 400, true},
		{"2,000 of 80,000: many, a small share", 2000, 80000, false},
		{"30,000 of 80,000", 30000, 80000, true},
	} {
		assert.Equal(t, tc.want, orphanGuardTrips(tc.orphans, tc.subjects), tc.name)
	}
}
