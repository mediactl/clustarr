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

// Package grabplan is the owner key's grab decision (ADR-0019 §6.6, §3.5
// P8, P13-P20, P23-P25): pure, it turns scored candidates -- an item's
// search answers in clustarr-searches, the RSS matcher's matches and a
// Search's picks from the candidate intake, and the delayed grabs status
// keeps -- into new Pending entries, a pending grab held for its delay, or
// nothing.
//
// The agents score and rank (pkg/decision.EvaluateRelease); Decide
// re-checks each approved candidate against the owner's fresh state with
// pkg/decision's cheap item-state rules (ItemStateRejections: the queue
// preference over the live entries, the current file, its cutoff and
// TranscodedFinal, the release index's block state and the owner's
// Blocklisted tombstones), its monitoring and the delay profile; narrows a
// pack to the episodes or issues that want it (P23); picks the best per
// coverage, passing over an indexer at its grab limit for the next one and
// holding the soonest when every approved one is limited (P13); and keeps
// the best delayed candidate in status (P16, replacing clustarr-pending and
// the scheduled grab message, P17-P18). One reconcile per owner at a time
// is the serialisation the KV grab lease used to be (§6.6 step 3).
//
// A person's pick (Candidate.Manual) is exempt from TranscodedFinal and
// from temporary rejections, as the Search controller's resolveGrab is;
// spec.override (Candidate.Override) from every permanent one, the
// Blocklisted tombstone included (§6.6 step 4).
//
// StillWants is lifecycle's convergence input (§6.7): whether an unclaimed
// transfer is still one the owner wants.
package grabplan
