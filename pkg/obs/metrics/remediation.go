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

package metrics

// Remediation loop telemetry (loop spec 2026-10-06 §3.19), manager only.
var (
	// RemediationPassesTotal counts loop passes by key kind and outcome
	// (applied, unchanged, conflict, stale, paced, error).
	RemediationPassesTotal = newCounterVec("clustarr_remediation_passes_total",
		"Remediation loop passes, by key kind and outcome.", "kind", "outcome")
	// RemediationPlannerFailuresTotal counts planner gather, plan and effect
	// failures by planner and reason (transient, cas_miss, error, panic).
	RemediationPlannerFailuresTotal = newCounterVec("clustarr_remediation_planner_failures_total",
		"Remediation planner failures, by planner and reason.", "planner", "reason")
	// RemediationPlannerSeconds is how long a planner's phase takes.
	RemediationPlannerSeconds = newHistogramVec("clustarr_remediation_planner_seconds",
		"Duration of a remediation planner phase in seconds, by planner and phase.", durationBucketsShort, "planner", "phase")
	// RemediationEffectsTotal counts effects by planner, effect kind and
	// outcome (ok, cas_miss, transient, error, panic).
	RemediationEffectsTotal = newCounterVec("clustarr_remediation_effects_total",
		"Remediation effects run, by planner, effect and outcome.", "planner", "effect", "outcome")
	// RemediationIOCallsTotal counts /data calls by op (stat, readdir) and
	// outcome (ok, timeout, saturated, breaker_open).
	RemediationIOCallsTotal = newCounterVec("clustarr_remediation_io_calls_total",
		"Remediation /data I/O calls, by op and outcome.", "op", "outcome")
)
