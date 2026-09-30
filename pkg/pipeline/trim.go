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

package pipeline

import "sort"

// Settled reports whether an entry at stage s holds a result rather than
// work in flight: done (Complete, Imported, SubtitleDone, TranscodeDone),
// idle (MetadataFound, MetadataSynced: nothing wanted of it), or stopped
// (Failed, Blocked). [Trim] keeps every entry that is not settled and only
// the newest settled ones.
func (s Stage) Settled() bool {
	switch s {
	case StageComplete, StageImported, StageSubtitleDone, StageTranscodeDone,
		StageMetadataFound, StageMetadataSynced, StageFailed, StageBlocked:
		return true
	default:
		return false
	}
}

// Trim returns every in-flight entry, in the order given, followed by the
// keep newest settled entries by Since, newest first. Failed and Blocked
// entries are results like any other: on the owner's library 1,429
// SubtitleRequests were Blocked, and pinning those would have kept the
// page as long as it was before any trim. keep 0 or less keeps in-flight
// entries only.
func Trim(entries []Entry, keep int) []Entry {
	var inFlight, settled []Entry
	for _, e := range entries {
		if e.Stage.Settled() {
			settled = append(settled, e)
		} else {
			inFlight = append(inFlight, e)
		}
	}
	sort.SliceStable(settled, func(i, j int) bool { return settled[i].Since.After(settled[j].Since) })
	if keep < 0 {
		keep = 0
	}
	if len(settled) > keep {
		settled = settled[:keep]
	}
	return append(inFlight, settled...)
}
