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

// Package rollup holds the pure functions the item reconcilers share to fold
// their files and grab entries into their own status, written once with one
// table test each and imported from every item package rather than
// duplicated between them. It began (task C6's controller amendment) as the
// Movie's and Episode's FileState and DownloadOverlay; it now carries the
// eight item kinds the remediation loop reconciles (Item, Watch): the grab
// entries' overlay and active entry (EntryOverlay, ActiveEntry), the
// transcoded and undecodable verdicts, the audio state, the catalog item and
// media-file events (ItemEvent, MediaFileEvent, Transitioned), and since the
// 2026-11-10 design the one phase and cutoffReason of the four non-video
// leaves (LeafPhase, LeafCutoffMet, LeafOverlay, LeafIdleAtStart,
// RetiredLeafConditions), the three containers' rollup (Container) and the
// kind-neutral Kubernetes Event recorder (Reporter).
//
// EntryOverlay's result is phase-independent (Overlay, not MoviePhase or
// EpisodePhase) because Movie and Episode each have their own phase enum;
// the movie and episode packages each carry a one-switch, no-logic adapter
// that maps an Overlay onto their own phase type, and the four leaves share
// LeafOverlay.
package rollup
