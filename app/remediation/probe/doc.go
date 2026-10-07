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

// Package probe is the remediation loop's probe planner (loop spec §3.5,
// §4.12; split §6.5.3): it judges a file's clustarr-probes record and
// incorporates, waits, or asks the import domain for a probe -- the request
// an effect after the status says ProbePending (§3.8). The decision is
// mediafile.PlanProbe; this package gathers its input and turns its outcome
// into the loop's Result.
package probe
