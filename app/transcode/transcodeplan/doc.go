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

// Package transcodeplan is the remediation loop's transcode planner, pure
// (loop spec 2026-10-06 §3.1, §5.8-§5.11; ADR-0016): which TranscodeProfile
// wins a file (the one selection rule, §2.13), whether the file belongs in
// its profile's window (Eligible), and what status.transcode becomes on a
// pass (Plan) -- legacy blocks normalised, verdicts held per (profileHash,
// probeHash), a window grant planned from the stored probe, a dispatch grant
// rendered Queued, the record incorporated, and a swap taken through
// Swapping with the tag it was dispatched under.
//
// It reads no client, no bus and no filesystem: app/remediation/transcode
// gathers what Plan reads and turns its decision into effects, and
// app/transcode/admission holds the window and the slots. It never imports
// admission: the adapter sees the ledger through [Admission], which
// *admission.Ledger implements.
package transcodeplan
