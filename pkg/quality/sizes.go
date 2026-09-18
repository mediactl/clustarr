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

package quality

// SizeLimit is the resolved min/preferred/max size in MB per runtime minute
// for one quality inside one Profile. A zero MaxMBPerMin means unlimited
// (TRaSH's own "0/2000 = unlimited" UI convention).
//
// Full docstring and the TRaSH size tables/SizeLimits function land later in
// this file; this minimal shape is introduced early because Profile.Sizes
// needs the type to compile.
type SizeLimit struct {
	MinMBPerMin  float64
	PrefMBPerMin float64
	MaxMBPerMin  float64
}
