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

// Package admission is the transcode admission ledger and its controller,
// transcode-admission (loop spec 2026-10-06 §5; ADR-0016): it decides which
// files enter each TranscodeProfile's window and which Planned files get a
// slot of which class, and remembers each pool's state. It writes nothing
// but memory -- its grants and directives are enqueued into the
// remediation loop's queue (S22, LoopSource), whose transcode planner acts
// on them against its own view and writes status.transcode -- and it is
// rebuilt from the apiserver, through the selectable status.transcode.phase,
// before it grants anything (§5.6), so a restart never over-admits.
//
// It never imports app/remediation: the loop imports it (for FileView and
// the ledger), and the transcode planner sees it through
// transcodeplan.Admission.
package admission
