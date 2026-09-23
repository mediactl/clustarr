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
package album

import (
	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
	"github.com/mediactl/clustarr/pkg/events"
)

// FileEdge is one MediaFileEvent a reconcile observes on an album: Action is
// events.ActionImported, ActionReplaced or ActionDeleted, File the
// MediaFile's name, and MediaFile the object itself -- nil for deleted,
// because by then it is gone.
type FileEdge struct {
	Action    string
	File      string
	MediaFile *catalogv1alpha1.MediaFile
}

// TrackFileTransitions reports the MediaFileEvents one reconcile observes as
// the album's track files move from old (status.tracks as last recorded) to
// tracks (the listing this reconcile declares). Each track's fileRef follows
// the Movie and Episode rule (rollup.FileTransition): imported when the
// track gains a file, replaced when another file takes it over, deleted when
// its file is gone -- Lidarr's per-track history (TrackFileImported,
// TrackFileDeleted; EntityHistoryEventType, Lidarr develop da7b4dfb).
// present is every MediaFile backing the album now.
//
// Only a file addressing one track (commonv1.MediaRef.Track) has a fileRef
// to follow; a file with no track is recorded nowhere in AlbumStatus, so no
// edge of it can be observed and it announces nothing.
//
// A change of selected release moves files on and off the listing without
// importing or deleting anything, so a fileRef that leaves the listing
// announces deleted only when its MediaFile is gone; one a newly selected
// release puts on the listing announces imported, the first time the album
// records it.
func TrackFileTransitions(old []catalogv1alpha1.Track, tracks []*catalogac.TrackApplyConfiguration, present []catalogv1alpha1.MediaFile) []FileEdge {
	byName := make(map[string]*catalogv1alpha1.MediaFile, len(present))
	for i := range present {
		byName[present[i].Name] = &present[i]
	}
	recorded := make(map[string]*string, len(old))
	for _, t := range old {
		recorded[t.RecordingID] = t.FileRef
	}

	var edges []FileEdge
	listed := make(map[string]bool, len(tracks))
	for _, t := range tracks {
		if t == nil || t.RecordingID == nil {
			continue
		}
		listed[*t.RecordingID] = true
		var mf *catalogv1alpha1.MediaFile
		if t.FileRef != nil {
			if mf = byName[*t.FileRef]; mf == nil {
				continue // a fileRef this reconcile did not list: nothing to announce it from
			}
		}
		action, file := rollup.FileTransition(recorded[*t.RecordingID], mf)
		if action == "" || (action == events.ActionDeleted && byName[file] != nil) {
			continue
		}
		edges = append(edges, FileEdge{Action: action, File: file, MediaFile: mf})
	}
	for _, t := range old {
		if listed[t.RecordingID] || t.FileRef == nil || byName[*t.FileRef] != nil {
			continue
		}
		edges = append(edges, FileEdge{Action: events.ActionDeleted, File: *t.FileRef})
	}
	return edges
}
