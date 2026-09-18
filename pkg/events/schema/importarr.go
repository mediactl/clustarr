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

package schema

// ScanTask asks a rescan worker to walk one RootFolder -- or the subpath of
// it a LibraryScan names -- on behalf of that LibraryScan. Subject:
// clustarr.work.importarr.scan.<rootfolder> (amendment §A1.4, §A1.6).
//
// The LibraryScan controller publishes exactly one of these per scan and then
// polls the worker's clustarr-progress checkpoint; it is the sole writer of
// LibraryScan.status, so the worker never reports back through the API
// server. Mode is a plain string rather than catalogv1alpha1.ScanMode because
// payloads here carry plain data only and never embed a custom resource's
// types (see this package's doc comment).
type ScanTask struct {
	// LibraryScanRef is the LibraryScan the walk is performed for. Its UID
	// keys the worker's progress checkpoint.
	LibraryScanRef Ref `json:"libraryScanRef"`

	// RootFolderRef is the RootFolder being walked.
	RootFolderRef Ref `json:"rootFolderRef"`

	// Path is the absolute directory to walk: RootFolder.spec.path joined
	// with LibraryScan.spec.subpath.
	Path string `json:"path"`

	// Mode is "full" or "incremental". An incremental walk skips a file
	// whose size and mtime still match the MediaFile that records it.
	Mode string `json:"mode"`

	// DryRun reports what would change without creating or updating
	// anything.
	DryRun bool `json:"dryRun,omitempty"`
}

// Schema implements Payload.
func (ScanTask) Schema() string { return "importarr.ScanTask.v1" }
