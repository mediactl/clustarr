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

// ListTask asks the import-list worker to sync one ImportList. Subject:
// clustarr.work.importarr.list.<importlist>, built by events.WorkListSubject
// and consumed by ConsumerImportList ("importarr-list", amendment §A1.6).
//
// It replaced catalog.ImportListTask and its subject,
// clustarr.work.catalogarr.importlist.normal.<uid>, which predated
// amendment-1's move of ImportList's sole controller from catalogarr to
// importarr (§A1.3). Nothing ever published that subject, and it was pruned
// with its consumer (catalogarr-importlist) in the gap-fix wave (X1).
// ListTask follows ScanTask's "importarr." schema prefix rather than
// catalog.go's "catalog." one for the same reason: this payload belongs to
// importarr, not catalogarr.
//
// The ImportList controller is the sole writer of ImportList.status (see
// k8s.ManagerImportarr's doc comment); the worker never patches it
// directly. Like LibraryScan/ScanTask, the worker instead checkpoints its
// result to a clustarr-progress key (see the importlist worker package's
// Result type) that the controller polls and projects into status -- so a
// task carries only the reference the worker needs to look the object back
// up, nothing the worker would otherwise have to report back through the
// apiserver.
type ListTask struct {
	// ListRef is the ImportList to sync.
	ListRef Ref `json:"listRef"`
}

// Schema implements Payload.
func (ListTask) Schema() string { return "importarr.ListTask.v1" }
