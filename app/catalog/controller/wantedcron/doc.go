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

// Package wantedcron is spec §6.1's "wantedcron (12h missing/cutoff-unmet
// scan with per-item >=6h gap and Attempts backoff 6h*2^n capped 7d)".
//
// It is a manager.Runnable, not a reconciler: there is no custom resource to
// reconcile, only a clock. Since ADR-0019 (§7.3, A4.2) it keeps only that
// timer: every twelve hours it lists the catalog, wakes each item whose
// search (or audio donor search) is due by its backoff in the remediation
// loop at low priority, and refills each namespace's budget of first-time
// live donor searches. The per-item decision -- due, IndexOnly, the donor
// cap, pacing and admission -- is the item key's search planner's
// (app/catalog/searchplan), which reads items through this package's
// Candidate, CandidateOf, Backoff and Eligible, so the sweep and the
// planner cannot disagree about what is wanted. Nothing is published: the
// WantedScan message and its expansion in the catalog agent retired.
//
// It is leader-elected: the loop it wakes is the leader's.
package wantedcron
