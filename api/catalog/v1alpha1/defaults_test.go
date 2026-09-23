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

package v1alpha1

import (
	"testing"

	"k8s.io/utils/ptr"
)

func TestCleanupDaysOrDefault(t *testing.T) {
	for _, c := range []struct {
		in   *int32
		want int32
	}{{nil, 7}, {ptr.To[int32](0), 0}, {ptr.To[int32](30), 30}} {
		if got := (RecycleBin{CleanupDays: c.in}).CleanupDaysOrDefault(); got != c.want {
			t.Errorf("CleanupDaysOrDefault(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}
