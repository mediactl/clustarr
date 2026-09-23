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
	// and conditions on every catalog.clustarr.io kind. (This comment used to
	// also claim Download.status.import, the project's one cross-group
	// status write. Design spec §8.4 assigned it to catalogarr before
	// amendment-1 moved the importer into importarr/worker/fileimport; see
	// ManagerImportarr, which now owns that write. Settled at task D2-7.)
	ManagerCatalogarr FieldManager = "catalogarr"

	// ManagerCatalogarrSeries is the catalogarr Series reconciler when it
	// writes to an Episode it owns. Its field set is exactly
	// EpisodeStatus' Title, Overview, AirDate, TvdbID, RuntimeMinutes and
	// AbsoluteNumber -- and nothing else. Not FinaleType, which this
	// comment used to list and which no writer sends (DesiredEpisode does
	// not even carry it); not SceneNumbering, which is M6 work; and not
	// spec.monitored, which ensureEpisode sets once, inside the Create,
	// under the client's own manager rather than this one.
	//
	// It is deliberately distinct from
	// ManagerCatalogarr, which the Episode reconciler uses for that Episode's own
	// status: server-side apply replaces a manager's whole ownership set on every
	// apply, so two writers sharing one manager name on one object silently
	// release each other's fields. Distinct managers make the split native.
	//
	// This comment used to promise that a double-claim would surface as a loud
	// apiserver conflict. It does not. PatchStatus and Apply both force
	// ownership (patch.go:88,121), which is what lets two managers co-own a
	// field on purpose -- but it also means the later applier silently takes
	// any field it claims. An over-claim is invisible in the object's values
	// and shows up only in metadata.managedFields, which is where a test that
	// means to catch one has to look.
	ManagerCatalogarrSeries FieldManager = "catalogarr-series"

	// ManagerCatalogarrWorker is the catalogarr queue worker. It covers the
	// consumers that write status fields NO other catalogarr writer touches:
	// the interactive search worker's Search.status.finishedAt/
	// indexerOutcomes/results, and the import and importlist consumers.
	//
	// The two consumers that used to share it and could not -- the metadata
	// gateway and the grab path -- have their own names below. See
	// ManagerCatalogarrGrab for what went wrong.
	ManagerCatalogarrWorker FieldManager = "catalogarr-worker"

	// ManagerCatalogarrMetadata is the catalogarr metadata gateway. On Movie
	// and Series it applies status.metadata and nothing else.
	//
	// It is deliberately distinct from ManagerCatalogarrGrab, which applies
	// status.activeDownloadRef/pendingGrab/lastSearchedAt/searchAttempts on
	// the same objects. Server-side apply replaces a manager's whole
	// ownership set on every apply rather than merging it, so while both
	// wrote as catalogarr-worker each one's apply RELEASED the other's
	// fields: a grab deleted the movie's cached metadata (dropping it to
	// Phase=Pending and forcing a provider refetch), and a metadata refresh
	// deleted status.activeDownloadRef and status.pendingGrab (dropping a
	// delayed item out of Phase=Delayed back to Wanted, where the wanted
	// cron re-searched an item that already had a grab scheduled). Both
	// directions were reproduced against a real apiserver; see
	// catalogarr/worker/grab's field-manager tests.
	ManagerCatalogarrMetadata FieldManager = "catalogarr-metadata"

	// ManagerCatalogarrGrab is the catalogarr grab path -- the grab worker,
	// the RSS matcher and the search worker's grab sink, which all write
	// through one code path. On Movie and Episode it applies exactly
	// status.activeDownloadRef, status.pendingGrab, status.lastSearchedAt and
	// status.searchAttempts.
	//
	// It never applies status.phase: the Movie and Episode reconcilers own
	// phase and conditions under ManagerCatalogarr, and recompute
	// Phase=Delayed from the pendingGrab this manager writes.
	//
	// See ManagerCatalogarrMetadata for why this is not
	// ManagerCatalogarrWorker.
	ManagerCatalogarrGrab FieldManager = "catalogarr-grab"

	// ManagerImportarr is the importarr controller manager. It owns ImportList,
	// ImportExclusion and LibraryScan status.
	//
	// It also owns Download.status.import -- the project's one cross-group
	// status write, applied by importarr/worker/fileimport (task D2-7), not
	// by any importarr controller. That write deliberately uses this bare
	// manager name rather than ManagerImportarrWorker: nothing else ever
	// applies under either name to a Download object, so there is no
	// collision to guard against the way there is on MediaFile (see
	// ManagerImportarrWorker), and grabarr/status.Patch (which owns every
	// other field manager on Download.status) refuses this name from its own
	// declaration precisely so that importarr's write stays importarr's,
	// made from importarr's own code, rather than being routed through
	// grabarr's declaration and picking up grabarr's owned set too.
	//
	// Design spec §8.4 assigned this write to catalogarr, under the name
	// ManagerCatalogarr. Amendment-1 moved the importer out of catalogarr
	// into importarr/worker/fileimport ("Nothing named 'importer' remains in
	// catalogarr"), and this field's ownership moved with it. Three comments
	// disagreed on the result until task D2-7 settled it: this one and
	// grabarr/status already said importarr; ManagerCatalogarr's comment and
	// DownloadStatus' own field doc still said catalogarr. All three are now
	// consistent.
	ManagerImportarr FieldManager = "importarr"

	// ManagerImportarrWorker is an importarr scan, list or file-import worker.
	// On MediaFile it applies MediaFileSpec only -- what it observed on disk,
	// plus the values frozen at import -- and never MediaFileStatus, which
	// catalogarr owns in full. The library-rescan worker also applies
	// MovieSpec under this name when it creates the Movie a scanned file is
	// attributed to. (This comment previously described a
	// status.file/status.probe split; those fields do not exist. See
	// CLAUDE.md's invariant.)
	//
	// It is deliberately distinct from ManagerImportarr even though the
	// workers run inside the same manager process as importarr's
	// controllers -- see rescan.FieldManager, which is the constant
	// production writes with and where the reasoning lives. Until task C14
	// the rescan worker actually wrote as ManagerImportarr while this
	// comment, catalogarr's two-writer gate and the e2e suite each said
	// something different.
	ManagerImportarrWorker FieldManager = "importarr-worker"

	// ManagerIndexarr is the indexarr controller manager. On Indexer it owns
	// the configuration half of status: conditions, protocol, privacy, caps,
	// observedGeneration and sessionSecretRef. IndexerDefinition and
	// IndexerProxy have one writer each, so it owns those outright.
	ManagerIndexarr FieldManager = "indexarr"

	// ManagerIndexarrWorker is indexarr's RSS poll and search fan-out. On
	// Indexer it owns the observed half of status: lastRssAt, lastRssNewCount,
	// indexedReleases, queriesInWindow, grabsInWindow, and the escalation
	// fields (failureLevel, initialFailureAt, disabledUntil, lastFailureAt,
	// lastFailureMsg).
	//
	// It is deliberately distinct from ManagerIndexarr because Indexer.status
	// has three writer paths -- the reconciler, the RSS poll and the search
	// fan-out -- which is one more than the design spec anticipated. Server-
	// side apply replaces a manager's whole ownership set on every apply, so
	// two paths sharing one manager name silently release each other's
	// fields; that hazard took eight distinct forms in Phase C and the
	// remedy that worked, twice, was distinct managers. The two worker paths
	// DO share this name, so both declare the identical set through
	// indexarr/status.WorkerFields -- one definition, not two.
	ManagerIndexarrWorker FieldManager = "indexarr-worker"

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
		ManagerCatalogarrSeries,
		ManagerCatalogarrWorker,
		ManagerCatalogarrMetadata,
		ManagerCatalogarrGrab,
		ManagerImportarr,
		ManagerImportarrWorker,
		ManagerIndexarr,
		ManagerIndexarrWorker,
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
