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

package k8s

import "fmt"

// FieldManager is a server-side-apply field-manager name. Section 2 of the
// design spec enumerates every manager Clustarr is allowed to use; §5 pins the
// exact status fields each one owns, so introducing a new name here without a
// matching spec change breaks the single-writer rule.
type FieldManager string

// The field managers from §2. Controllers use the bare service name; workers
// and engines use the suffixed name and may only apply the disjoint subset of
// status fields the spec assigns them.
const (
	// ManagerCatalogarr is the catalogarr controller manager. It owns phase
	// and conditions on every catalog.clustarr.io kind and is the only
	// cross-group status writer (Download.status.import).
	ManagerCatalogarr FieldManager = "catalogarr"

	// ManagerCatalogarrWorker is the catalogarr queue worker, covering the
	// search, grab, import, importlist and rss-matcher consumers.
	ManagerCatalogarrWorker FieldManager = "catalogarr-worker"

	// ManagerImportarr is the importarr controller manager. It owns ImportList,
	// ImportExclusion and LibraryScan status, and Download.status.import.
	ManagerImportarr FieldManager = "importarr"

	// ManagerImportarrWorker is an importarr scan, list or file-import worker. On
	// MediaFile it applies MediaFileSpec only -- what it observed on disk, plus
	// the values frozen at import -- and never MediaFileStatus, which catalogarr
	// owns in full. (This comment previously described a status.file/status.probe
	// split; those fields do not exist. See CLAUDE.md's invariant.)
	ManagerImportarrWorker FieldManager = "importarr-worker"

	// ManagerIndexarr is the indexarr manager, the single writer for
	// index.clustarr.io.
	ManagerIndexarr FieldManager = "indexarr"

	// ManagerGrabarr is the grabarr controller manager, the single writer for
	// download.clustarr.io phase and conditions.
	ManagerGrabarr FieldManager = "grabarr"

	// ManagerGrabarrEngine is a torrent or usenet engine pod. It applies only
	// the telemetry fields of Download.status.
	ManagerGrabarrEngine FieldManager = "grabarr-engine"

	// ManagerSquasharr is the squasharr controller manager.
	ManagerSquasharr FieldManager = "squasharr"

	// ManagerSquasharrWorker is a squasharr transcode Job pod.
	ManagerSquasharrWorker FieldManager = "squasharr-worker"

	// ManagerCaptionarr is the captionarr controller manager.
	ManagerCaptionarr FieldManager = "captionarr"

	// ManagerCaptionarrWorker is a captionarr subtitle fetch worker. It
	// applies SubtitleRequest.status.items entries only.
	ManagerCaptionarrWorker FieldManager = "captionarr-worker"
)

// FieldManagers lists every manager name §2 allows, in spec order.
func FieldManagers() []FieldManager {
	return []FieldManager{
		ManagerCatalogarr,
		ManagerCatalogarrWorker,
		ManagerImportarr,
		ManagerImportarrWorker,
		ManagerIndexarr,
		ManagerGrabarr,
		ManagerGrabarrEngine,
		ManagerSquasharr,
		ManagerSquasharrWorker,
		ManagerCaptionarr,
		ManagerCaptionarrWorker,
	}
}

// String returns the manager name as the apiserver sees it.
func (f FieldManager) String() string { return string(f) }

// Valid reports whether f is one of the names §2 enumerates.
func (f FieldManager) Valid() bool {
	for _, known := range FieldManagers() {
		if f == known {
			return true
		}
	}
	return false
}

// Validate returns an error naming f when it is not a manager from §2.
func (f FieldManager) Validate() error {
	if f == "" {
		return fmt.Errorf("k8s: empty field manager")
	}
	if !f.Valid() {
		return fmt.Errorf("k8s: %q is not a field manager listed in the design spec", f)
	}
	return nil
}
