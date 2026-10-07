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

// Package scanapply is the manager's scan applier (ADR-0019 §7.6, A6.1):
// the intake.ScanApplier the leader-only import-intake-scan consumer hands
// every library-scan observation to. The rescan agent
// (app/import/worker/rescan) walks, parses, fingerprints and attributes,
// and reports; this package decides and writes:
//
//   - present, attributed: create the Movie or Series the attribution
//     proposes when absent (names.Movie / names.Series under import-worker,
//     addOptions.monitor none) unless an ImportExclusion names its id (P37)
//     or the root folder sets no default profile (P38), never overwriting
//     an existing object whose provider id differs (unmatched,
//     nameCollision); validate a person's manual target again (P45); refuse
//     to re-point a path recorded against another item (P41,
//     recorded_elsewhere); record the MediaFile spec through
//     specwrite.ApplyFacts under import-worker, re-asserting an existing
//     one's frozen fields; and hand a post-transcode change to catalog by
//     the observed-fingerprint annotation under import (P42, W17).
//   - present, a transcode's output (P44): never adopted, only counted.
//   - stale basis: the MediaFile the agent read moved since (its spec no
//     longer hashes to the observation's basis): nothing is applied; the
//     file's folder is re-observed by a LibraryScan of its own, at most one
//     per folder per ReobserveWindow, and the file is counted Deferred.
//   - missing: delete the MediaFile with UID and resourceVersion
//     preconditions once a fresh read still has it FileMissing for 10
//     minutes (P46).
//   - orphanPart: a transcode attempt's part file unwritten for 72 h whose
//     source has no transcode in flight and whose job is not live is
//     recycled through a recycle-files task on import-recycle (P47).
//   - unmatched: the agent's tally already lists it; nothing to apply.
//
// The scanner never guesses: an attribution the agent could not make
// arrives as unmatched, and every refusal here is listed as unmatched with
// its reason in the applier's tally (scanprogress.AppliedKey), which the
// LibraryScan controller adds to the walk's. Creates, updates and deletes
// pass the remediation loop's write limiter (25/s), waiting rather than
// failing. Apply is idempotent: a crash between the write and the ack
// re-applies, and a redelivered observation whose write already landed
// reads as applied, not stale.
//
// Only cmd/manager links this package; no agent root may (A7, §9.2).
package scanapply
