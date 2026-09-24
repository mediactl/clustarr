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
package audiobook

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
)

// FileEdge is one MediaFileEvent a reconcile observes on an audiobook:
// Action is events.ActionImported or events.ActionDeleted, File the
// MediaFile's name, and MediaFile the object itself -- nil for deleted,
// because by then it is gone.
type FileEdge struct {
	Action    string
	File      string
	MediaFile *catalogv1alpha1.MediaFile
}

// FileTransitions reports the MediaFileEvents one reconcile observes as the
// audiobook's parts move from oldRefs (status.fileRefs as last recorded) to
// newRefs (FileState's fileRefs now): imported for each part newly backing
// it, in play order, then deleted for each recorded part whose MediaFile is
// gone. present is every MediaFile backing the audiobook now.
//
// There is no replaced edge, unlike the one-file kinds' rollup.FileTransition.
// Which part of a new release replaces which part of the old one is not
// knowable (importarr's fileimport supersede says the same), and Readarr,
// whose unit this is, records a multi-file book's history per file as
// BookFileImported and BookFileDeleted, with no replace event at all
// (EntityHistoryEventType, Readarr develop 0b79d300).
//
// A recorded part that has left newRefs but whose MediaFile still exists --
// possible only past FileState's 200-ref cap -- was not deleted, and
// announces nothing.
func FileTransitions(oldRefs, newRefs []string, present []catalogv1alpha1.MediaFile) []FileEdge {
	byName := make(map[string]*catalogv1alpha1.MediaFile, len(present))
	for i := range present {
		byName[present[i].Name] = &present[i]
	}
	recorded := make(map[string]bool, len(oldRefs))
	for _, ref := range oldRefs {
		recorded[ref] = true
	}
	var edges []FileEdge
	for _, ref := range newRefs {
		if !recorded[ref] {
			edges = append(edges, FileEdge{Action: events.ActionImported, File: ref, MediaFile: byName[ref]})
		}
	}
	for _, ref := range oldRefs {
		if byName[ref] == nil {
			edges = append(edges, FileEdge{Action: events.ActionDeleted, File: ref})
		}
	}
	return edges
}
