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

package rollup

import (
	"hash/fnv"
	"slices"
	"strconv"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// The entry forms of the Download rollups (ADR-0019 §6.11): the same rules
// over an owner's status.downloads[] as DownloadNonTerminal, ActiveDownload,
// DonorDownloading and DownloadOverlay have over Download objects, which stay
// until the grab worker and the search worker's queue stop reading
// Downloads (A4.4).

// EntryNonTerminal reports whether an entry can still deliver content to
// its owner: DownloadNonTerminal's rule over an entry. Imported, Failed,
// Blocklisted and Removing are terminal, and so is a held import (import
// phase held or expired): the entry keeps its files for a person's decision
// but nothing more comes of it on its own, so a transcoded movie reads
// Transcoded, not Downloading, while it waits. Completed and Seeding are not
// terminal: the content is one import away.
func EntryNonTerminal(e *catalogv1alpha1.DownloadEntry) bool {
	if e == nil {
		return false
	}
	if imp := e.Import; imp != nil && (imp.Phase == catalogv1alpha1.ImportPhaseHeld || imp.Phase == catalogv1alpha1.ImportPhaseExpired) {
		return false
	}
	switch e.Phase {
	case commonv1.DownloadPhaseImported, commonv1.DownloadPhaseFailed,
		commonv1.DownloadPhaseBlocklisted, commonv1.DownloadPhaseRemoving:
		return false
	}
	return true
}

// IsDonor reports an audio donor entry (anime dual-audio spec §6.1).
func IsDonor(e *catalogv1alpha1.DownloadEntry) bool {
	return e != nil && e.Purpose == commonv1.DownloadPurposeAudioDonor
}

// ActiveEntry picks the entry an item's activeDownloadRef names among the
// entries covers passes (nil passes all): the oldest non-terminal one by
// grabbedAt, the id breaking a tie; donors are skipped, since a donor moves
// neither the ref nor the phase. nil means none.
func ActiveEntry(entries []catalogv1alpha1.DownloadEntry, covers func(*catalogv1alpha1.DownloadEntry) bool) *catalogv1alpha1.DownloadEntry {
	var best *catalogv1alpha1.DownloadEntry
	for i := range entries {
		e := &entries[i]
		if !EntryNonTerminal(e) || IsDonor(e) || (covers != nil && !covers(e)) {
			continue
		}
		if best == nil || entryOlder(e, best) {
			best = e
		}
	}
	return best
}

// DonorEntryOpen reports an open audio donor entry among those covers
// passes: status.audio.graft's grabbed.
func DonorEntryOpen(entries []catalogv1alpha1.DownloadEntry, covers func(*catalogv1alpha1.DownloadEntry) bool) bool {
	for i := range entries {
		e := &entries[i]
		if IsDonor(e) && EntryNonTerminal(e) && (covers == nil || covers(e)) {
			return true
		}
	}
	return false
}

// EntryOverlay is DownloadOverlay's table over an entry's phase: every
// non-terminal entry overlays Downloading and is active; every terminal one
// has no opinion.
func EntryOverlay(e *catalogv1alpha1.DownloadEntry) (Overlay, bool) {
	if !EntryNonTerminal(e) {
		return OverlayNone, false
	}
	return OverlayDownloading, true
}

// EntryPhase is the downloadPhase an owner shows: the active entry's phase,
// "" when none.
func EntryPhase(entries []catalogv1alpha1.DownloadEntry) commonv1.DownloadPhase {
	if e := ActiveEntry(entries, nil); e != nil {
		return e.Phase
	}
	return ""
}

func entryOlder(a, b *catalogv1alpha1.DownloadEntry) bool {
	at, bt := a.GrabbedAt.Time, b.GrabbedAt.Time
	if !at.Equal(bt) {
		return at.Before(bt)
	}
	return a.ID < b.ID
}

// CoversEpisode reports whether a Series entry covers the episode
// (season, number) in the Series' stored order. Every Series entry records
// the numbers it covers -- a single-episode grab one, a pack each of its
// episodes, an adopted Download its target's (A8.1) -- so the Episode's
// name is not needed to decide; it is taken for the callers' symmetry with
// CoversIssue and for logs.
func CoversEpisode(e *catalogv1alpha1.DownloadEntry, _ string, season, number int32) bool {
	if e == nil {
		return false
	}
	for _, n := range e.Episodes {
		if n.Season == season && n.Number == number {
			return true
		}
	}
	return false
}

// CoversIssue reports whether a Comic entry covers the issue number.
func CoversIssue(e *catalogv1alpha1.DownloadEntry, _, number string) bool {
	return e != nil && number != "" && slices.Contains(e.Issues, number)
}

// DownloadsSignature hashes what an Episode's or Issue's view reads of its
// container (S24, ADR-0019 §6.11): each entry's id, phase and covered
// numbers, and each pending grab's covered numbers. A status change that
// leaves it equal wakes no covered key.
func DownloadsSignature(entries []catalogv1alpha1.DownloadEntry, pending []catalogv1alpha1.PendingGrab) uint64 {
	h := fnv.New64a()
	w := func(s string) {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	for i := range entries {
		e := &entries[i]
		w("e")
		w(e.ID)
		w(string(e.Phase))
		w(string(e.Purpose))
		if e.Import != nil {
			w(string(e.Import.Phase))
		}
		for _, n := range e.Episodes {
			w(strconv.Itoa(int(n.Season)) + "x" + strconv.Itoa(int(n.Number)))
		}
		for _, n := range e.Issues {
			w(n)
		}
	}
	for i := range pending {
		p := &pending[i]
		w("p")
		w(p.GrabAt.UTC().String())
		for _, n := range p.Episodes {
			w(strconv.Itoa(int(n.Season)) + "x" + strconv.Itoa(int(n.Number)))
		}
		for _, n := range p.Issues {
			w(n)
		}
	}
	return h.Sum64()
}

// CoveredEpisodes is every (season, number) the entries and pending grabs
// name, each once.
func CoveredEpisodes(entries []catalogv1alpha1.DownloadEntry, pending []catalogv1alpha1.PendingGrab) []catalogv1alpha1.EpisodeNumber {
	var out []catalogv1alpha1.EpisodeNumber
	add := func(ns []catalogv1alpha1.EpisodeNumber) {
		for _, n := range ns {
			if !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	for i := range entries {
		add(entries[i].Episodes)
	}
	for i := range pending {
		add(pending[i].Episodes)
	}
	return out
}

// CoveredIssues is every issue number the entries and pending grabs name,
// each once.
func CoveredIssues(entries []catalogv1alpha1.DownloadEntry, pending []catalogv1alpha1.PendingGrab) []string {
	var out []string
	add := func(ns []string) {
		for _, n := range ns {
			if !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	for i := range entries {
		add(entries[i].Issues)
	}
	for i := range pending {
		add(pending[i].Issues)
	}
	return out
}
