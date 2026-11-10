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

package views

// clampPercent keeps a progress bar's width inside [0, 100] even if a caller
// passes Entry's "no percentage" sentinel (-1) or an out-of-range value.
func clampPercent(p int32) int32 {
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	default:
		return p
	}
}

// audioTone is an AudioNote's badge colour.
func audioTone(n AudioNote) string {
	if n.Warn {
		return "text-amber-600 dark:text-amber-400"
	}
	return "text-muted-foreground"
}
