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

// Package importplan decides a completed grab's import in the manager
// (ADR-0019 §6.9; spec §3.5 P51-P69): pure functions over the import
// agent's inspect and execute records, the owner's grab entry and the
// MediaFiles the cache holds. It never reads a client, the bus, a clock or
// the filesystem: the downloads stage gathers an Input and Decide answers a
// lifecycle.ImportDecision -- the inspect or execute task owed, the
// MediaFiles to materialise and delete, and the verdict lifecycle reads.
//
// The rules are fileimport's, moved: only Completed entries import; the
// download.clustarr.io/import intent redirects the target and marks
// override; the quality profile is the entry's; a suspected sample is a
// person's call unless real media is beside it; the best file fills each
// single-file item (quality, revision, size); a probe-corrected quality the
// profile does not allow is the release's fault; an automatic grab never
// replaces a transcoded file (MessageExistingFileFinal); a file must be an
// upgrade over each it replaces unless it carries the language a
// wrong-language file lacks; a multi-file item with files needs a person,
// and a manual one is replaced all or nothing. An import that imports
// nothing is classed (Classify) and retried, held or blocked by its class:
// transient at 1 m, 10 m and 45 m, then held; a release fault inspected once
// more, then blocklisted; itemState and needsPerson held at once; a hold
// expires after lifecycle.ImportHoldRetention, never blocklisted.
//
// A missing record is never a verdict: an unanswered task is republished
// under its Msg-Id inside RepublishWindow and re-issued at a new seq past
// it, and a summary whose record is gone re-issues the inspect.
//
// Destinations are rendered by the inspect, which holds the full probe
// pkg/naming's tokens read; the planner checks them (containment,
// duplicates) and names each MediaFile (k8s.ChildName(target, "mediafile",
// dest)).
package importplan
