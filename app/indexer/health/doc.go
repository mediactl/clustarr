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

// Package health is the index agent's half of indexer health (ADR-0019
// §7.5, design §3.1 W34-W35, §3.5 P87): it reports every query's outcome
// -- a search fan-out query or an RSS poll -- in the clustarr-indexer-health
// records bucket, and suppresses a failing indexer locally until the
// manager's verdict.
//
// The agent decides nothing about backoff. The Indexer controller (manager)
// reads each record, runs Prowlarr's escalation ladder
// (app/indexer/status's RecordFailure and RecordSuccess) and writes the
// result to Indexer.status; the agent reads status.disabledUntil from its
// cache before it queries, as it always has (P82). [Writer] is the one
// writer of an indexer's record in the agent process: the fan-out and the
// RSS poll share it, so their read-modify-writes are one in-process
// compare-and-swap (records.Writer.Update) and neither rolls back the
// other's counters.
//
// [Suppress] is a mechanism, not a policy: between a failure and the
// manager's verdict (well under a second when the manager is up) a burst
// of searches would otherwise all hit an indexer that is failing, so the
// agent skips it for at most [SuppressFor]. The verdict -- status naming
// that failure -- ends the suppression early.
//
// Nothing here links a Kubernetes writer: the package is reached from the
// agent root (A7.1's guard).
package health
