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
package lifecycle

import (
	"fmt"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// A set's entries (a movie released in parts, entry.set): every part
// downloads as its own transfer; part 1 imports them all, joined, once each
// has completed (SetReady, the downloads stage's gate on its import); the
// others wait and follow it; a part that fails takes the rest down.

// SetReady reports whether every part of e's set is among entries and done
// (Importable, its record by rec): the first part's import may run. False
// for an entry of no set.
func SetReady(
	entries []catalogv1alpha1.DownloadEntry, e *catalogv1alpha1.DownloadEntry,
	rec func(*catalogv1alpha1.DownloadEntry) *schema.TransferRecord,
) bool {
	done, of := setDone(entries, e, rec)
	return of > 0 && done == of
}

// setDone counts e's set's parts among entries that are done, and the
// set's size; 0, 0 for an entry of no set.
func setDone(
	entries []catalogv1alpha1.DownloadEntry, e *catalogv1alpha1.DownloadEntry,
	rec func(*catalogv1alpha1.DownloadEntry) *schema.TransferRecord,
) (done, of int) {
	if e.Set == nil {
		return 0, 0
	}
	for i := range entries {
		s := &entries[i]
		if s.Set != nil && s.Set.ID == e.Set.ID && Importable(s, rec(s)) {
			done++
		}
	}
	return done, int(e.Set.Of)
}

// sameSet are e's set's other parts in the pass's entries.
func (d *decider) sameSet(e *catalogv1alpha1.DownloadEntry) []*catalogv1alpha1.DownloadEntry {
	if e.Set == nil {
		return nil
	}
	var out []*catalogv1alpha1.DownloadEntry
	for i := range d.all {
		s := &d.all[i]
		if s.UID != e.UID && s.Set != nil && s.Set.ID == e.Set.ID {
			out = append(out, s)
		}
	}
	return out
}

// setFailure is why e's set can no longer import, "" while it can: another
// part failed, was blocklisted or is being removed unimported, or is gone.
// An imported part has nothing left to fail.
func (d *decider) setFailure(e *catalogv1alpha1.DownloadEntry) string {
	if e.Set == nil || imported(e) {
		return ""
	}
	others := d.sameSet(e)
	for _, s := range others {
		switch {
		case s.Phase == commonv1.DownloadPhaseFailed || s.Phase == commonv1.DownloadPhaseBlocklisted:
			return fmt.Sprintf("part %d of its set failed", s.Set.Part)
		case s.Phase == commonv1.DownloadPhaseRemoving && !imported(s):
			return fmt.Sprintf("part %d of its set is being removed", s.Set.Part)
		}
	}
	if len(others)+1 < int(e.Set.Of) {
		return "a part of its set is gone"
	}
	return ""
}

// setImport is a Completed part's import step: the first part imports the
// set once every part is done (importStep); another follows the first,
// Imported once it is. It reports a transition that ended the pass.
func (d *decider) setImport(e *catalogv1alpha1.DownloadEntry) bool {
	if e.Set.Part != 1 {
		for _, s := range d.sameSet(e) {
			if s.Set.Part == 1 && imported(s) {
				d.toImported(e)
				return true
			}
		}
		e.Message = "waiting for part 1 to import the set"
		return false
	}
	if done, of := setDone(d.all, e, d.recordOf); done < of {
		e.Message = clampString(fmt.Sprintf("waiting for the set's other parts: %d of %d downloaded", done, of), MaxEntryMessage)
		return false
	}
	return d.importStep(e)
}
