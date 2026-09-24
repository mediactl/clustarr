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

package worker

// Process-level exit codes of cmd/squasharr-worker, distinct from the task
// classifications in run.go: those end up in a task.Result, never in a pod's
// exit code.
const (
	// WorkerExitRetriable: NATS unreachable, ffmpeg missing -- worker-level
	// setup failed for a reason that may not recur, and the pool Job should
	// keep the pod restarting.
	WorkerExitRetriable = 2

	// WorkerExitMisconfigured: bad or missing environment. The pool Job
	// fails outright rather than restarting into the same misconfiguration.
	WorkerExitMisconfigured = 3

	// WorkerExitDrained: SIGTERM. podFailurePolicy ignores it, so scaling a
	// pool down never counts against the Job.
	WorkerExitDrained = 10
)
