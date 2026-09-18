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

package release

import "time"

// Age returns publishedAt's age relative to now at three granularities,
// mirroring Radarr's ReleaseResource.{age,ageHours,ageMinutes} (all
// integers in the source API — see docs/research/naming.md A5 — so no float
// is introduced here). now before publishedAt returns all zeros.
func Age(publishedAt, now time.Time) (days int32, hours int32, minutes int64) {
	d := now.Sub(publishedAt)
	if d <= 0 {
		return 0, 0, 0
	}
	minutes = int64(d / time.Minute)
	hours = int32(d / time.Hour)
	days = int32(d / (24 * time.Hour))
	return days, hours, minutes
}

// SizePerMinuteCentiMB returns sizeBytes expressed as hundredths of a
// megabyte per minute of runtime (a scaled int, matching CLAUDE.md's Centis
// convention — pkg/quality.Definition's own MinMBPerMin et al. are float64
// because that struct lives outside api/ too, but this package still avoids
// float in its own exported surface). runtimeMinutes <= 0 (unknown runtime)
// returns -1.
func SizePerMinuteCentiMB(sizeBytes int64, runtimeMinutes int32) int64 {
	if runtimeMinutes <= 0 {
		return -1
	}
	const bytesPerMB = 1024 * 1024
	// (sizeBytes * 100) / (bytesPerMB * runtimeMinutes), reordered to divide last.
	return sizeBytes * 100 / (bytesPerMB * int64(runtimeMinutes))
}
