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

// Package graftplan is the remediation loop's graft planner (loop spec
// §2.6, §4.12, §5.13; anime dual-audio spec §7.2): pure, so it reads what
// the adapter (app/remediation/graft) gathered and writes only
// status.graft, status.graftTag and status.graftedAt. A file whose item has
// a donor and lacks a language waits for its file's transcode (to graft in
// the same pass, at most JoinWait), then for a graft slot the admission
// ledger grants, and is then dispatched as a standalone graft Job that
// answers in clustarr-grafts. It also gives the transcode planner the graft
// a transcode dispatch carries (Join) and says when to hold a dispatch for
// a joined result not yet recorded (JoinPending). It imports no client, no
// bus and no app/remediation.
package graftplan
