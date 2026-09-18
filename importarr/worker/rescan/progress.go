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

package rescan

import (
	"encoding/json"
	"fmt"
	"time"
)

// UnmatchedFile is one file the walk could not attribute to a catalog item.
// It mirrors catalogv1alpha1.UnmatchedFile without importing the CRD
// package: a Progress value travels through a KV entry rather than the
// apiserver, and keeps the same "plain data only" discipline bus payloads
// use.
//
// Path is relative to the RootFolder being walked, matching the CRD field's
// own contract ("the file path relative to the root folder").
type UnmatchedFile struct {
	// Path is the file, relative to the root folder that was walked.
	Path string `json:"path"`

	// Reason explains, in words a user can act on, why the file could not
	// be attributed.
	Reason string `json:"reason"`

	// Candidates lists the catalog items the scanner considered but could
	// not choose between. Capped at [MaxCandidates] by the caller, which is
	// the CRD's own MaxItems.
	Candidates []string `json:"candidates,omitempty"`

	// SeenAt is when the walk observed the file.
	SeenAt time.Time `json:"seenAt"`
}

// Progress is the worker's running -- and, once Done, final -- tally for one
// LibraryScan. The worker checkpoints it to a clustarr-progress key roughly
// every [checkpointInterval]; the LibraryScan controller polls that key and
// aggregates it into status, because the controller is the single writer of
// LibraryScan.status and the single-writer rule has no worker exception
// here.
type Progress struct {
	// Done is true once the walk has finished, successfully or not. Until
	// then the controller keeps the scan in the Running phase.
	Done bool `json:"done"`

	// Error is non-empty when the walk failed; the controller turns it into
	// the Failed phase and a False Ready condition.
	Error string `json:"error,omitempty"`

	// FilesSeen counts files classified as media and therefore considered
	// for attribution.
	FilesSeen int64 `json:"filesSeen"`

	// FilesMatched counts files attributed to a catalog item.
	FilesMatched int64 `json:"filesMatched"`

	// ItemsCreated counts catalog items the walk created.
	ItemsCreated int64 `json:"itemsCreated"`

	// ItemsUpdated counts catalog items the walk updated.
	ItemsUpdated int64 `json:"itemsUpdated"`

	// FilesSkipped counts files the walk deliberately did not consider: a
	// sample, extra, part or non-media file, an unchanged incremental
	// fingerprint, or a file catalogarr owns post-transcode.
	FilesSkipped int64 `json:"filesSkipped"`

	// Unmatched lists the files that could not be attributed.
	Unmatched []UnmatchedFile `json:"unmatched,omitempty"`
}

// Encode marshals p for a KV Put.
func (p Progress) Encode() ([]byte, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("rescan: encode progress: %w", err)
	}
	return data, nil
}

// DecodeProgress unmarshals a KV value written by [Progress.Encode].
func DecodeProgress(data []byte) (Progress, error) {
	var p Progress
	if err := json.Unmarshal(data, &p); err != nil {
		return Progress{}, fmt.Errorf("rescan: decode progress: %w", err)
	}
	return p, nil
}

// ProgressKey is the clustarr-progress key one LibraryScan's worker
// checkpoints to and its controller polls, following that bucket's existing
// "<kind>.<uid>" convention (spec §5's clustarr-progress row). The UID, not
// the name, keys it: a scan deleted and recreated under the same name is a
// different scan and must not read the old one's tally.
func ProgressKey(scanUID string) string { return "scan." + scanUID }
