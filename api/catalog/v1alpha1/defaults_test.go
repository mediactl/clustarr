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
)

func TestCleanupDaysOrDefault(t *testing.T) {
	for _, c := range []struct {
		in   *int32
		want int32
	}{{nil, 7}, {new(int32), 0}, {func() *int32 { i := int32(30); return &i }(), 30}} {
		if got := (RecycleBin{CleanupDays: c.in}).CleanupDaysOrDefault(); got != c.want {
			t.Errorf("CleanupDaysOrDefault(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestOverlayGeometryOrDefault(t *testing.T) {
	// A nil *OverlayGeometry (spec.geometry unset entirely) and a non-nil
	// OverlayGeometry whose own fields are nil (an explicit but empty
	// geometry) must both read as the documented defaults.
	var nilGeometry *OverlayGeometry
	empty := &OverlayGeometry{}
	set := &OverlayGeometry{
		WidthPercent:   new(int32),
		RadiusPercent:  new(int32),
		PaddingPercent: new(int32),
		LogoPercent:    new(int32),
		ScorePercent:   new(int32),
		OpacityPercent: new(int32),
	}

	for _, c := range []struct {
		name string
		got  int32
		want int32
	}{
		{"nil/width", nilGeometry.WidthPercentOrDefault(), 19},
		{"nil/radius", nilGeometry.RadiusPercentOrDefault(), 2},
		{"nil/padding", nilGeometry.PaddingPercentOrDefault(), 2},
		{"nil/logo", nilGeometry.LogoPercentOrDefault(), 60},
		{"nil/score", nilGeometry.ScorePercentOrDefault(), 27},
		{"nil/opacity", nilGeometry.OpacityPercentOrDefault(), 80},

		{"empty/width", empty.WidthPercentOrDefault(), 19},
		{"empty/radius", empty.RadiusPercentOrDefault(), 2},
		{"empty/padding", empty.PaddingPercentOrDefault(), 2},
		{"empty/logo", empty.LogoPercentOrDefault(), 60},
		{"empty/score", empty.ScorePercentOrDefault(), 27},
		{"empty/opacity", empty.OpacityPercentOrDefault(), 80},

		{"set/width", set.WidthPercentOrDefault(), 20},
		{"set/radius", set.RadiusPercentOrDefault(), 3},
		{"set/padding", set.PaddingPercentOrDefault(), 4},
		{"set/logo", set.LogoPercentOrDefault(), 70},
		{"set/score", set.ScorePercentOrDefault(), 50},
		{"set/opacity", set.OpacityPercentOrDefault(), 100},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}
