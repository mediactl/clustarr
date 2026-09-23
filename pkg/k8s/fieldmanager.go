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
	// indexerOutcomes/results, and the import consumer. (The importlist
	// consumer this comment also named was pre-amendment dead code, pruned in
	// the gap-fix wave: import lists are importarr's.)
	//
	// The two consumers that used to share it and could not -- the metadata
	// gateway and the grab path -- have their own names below. See
	// ManagerCatalogarrGrab for what went wrong.
	ManagerCatalogarrWorker FieldManager = "catalogarr-worker"

	// ManagerCatalogarrMetadata is the catalogarr metadata gateway. On Movie
	// and Series it applies status.metadata and nothing else.
	//
	// It is deliberately distinct from ManagerCatalogarrGrab, which applies
	// status.pendingGrab/lastSearchedAt/searchAttempts on the same objects. Server-side apply replaces a manager's whole
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
	// through one code path. On a catalog item it applies exactly
	// status.pendingGrab, status.lastSearchedAt and status.searchAttempts.
	//
	// It does NOT own status.activeDownloadRef (gap-fix ruling R-5). That
	// field has one writer: the item's own reconciler, under
	// ManagerCatalogarr, which derives it level-style from the item's owned
	// non-terminal Download. The grab path's double-grab guard looks up the
	// existing Downloads for the target instead of reading the ref. The grab
	// path used to apply the ref as well, so the two managers co-owned it
	// under ForceOwnership and ownership migrated to whichever applied last;
	// the reconciler's "omit to clear on a terminal Download" only worked
	// while it happened to hold the field. Until task X4a lands,
	// catalogarr/worker/grab still applies it (kindops.go); that write is the
	// defect R-5 removes, not a second sanctioned owner.
	//
	// It never applies status.phase: the reconcilers own phase and
	// conditions under ManagerCatalogarr, and recompute Phase=Delayed from
	// the pendingGrab this manager writes.
	//
	// See ManagerCatalogarrMetadata for why this is not
	// ManagerCatalogarrWorker.
	ManagerCatalogarrGrab FieldManager = "catalogarr-grab"

	// ManagerCatalogarrFanout is the Comic reconciler when it writes an
	// Issue's provider-sourced status fields (sourceID, title, date) onto the
	// Issues it owns -- the role ManagerCatalogarrSeries plays for
	// Series->Episode.
	//
	// Comic->Issue is the only non-video pair that uses it. It was declared
	// (task G1-0) for Artist->Album and Author->Book as well, but G2-1
	// (commit f665aa9) settled that neither of those parents writes onto its
	// children at all: Album and Book are metadata targets of their own, so
	// the gateway fills their status.metadata under ManagerCatalogarrMetadata
	// from the same provider call a fan-out write would copy, and a second
	// writer of identical values is the co-ownership trap that hides an SSA
	// release. The Artist and Author reconcilers create their children with
	// spec fields only and never apply to them again (their doc.go files say
	// why). Issue, like Episode, has no status.metadata of its own, so its
	// provider fields have nowhere else to come from.
	//
	// It is deliberately distinct from ManagerCatalogarr, which the Issue
	// reconciler uses for that Issue's own status (state, conditions,
	// hasFile, fileRef, fileQuality, activeDownloadRef), exactly as
	// Episode's reconciler uses ManagerCatalogarr for its own
	// Phase/Conditions/HasFile rather than ManagerCatalogarrSeries.
	// Server-side apply replaces a manager's whole ownership set on every
	// apply, so a parent and its child sharing one manager name on the child
	// object would silently release each other's fields the next time
	// either side reconciled -- proven once already for Series/Episode (see
	// ManagerCatalogarrSeries).
	ManagerCatalogarrFanout FieldManager = "catalogarr-fanout"

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

	// ManagerDLQProjector is the DLQ projector wired into catalogarr's
	// RoleHistory branch (built by task G1-4, wired by G1-5), the one
	// subscriber to events.ConsumerDLQProjector. Design spec §5 originally
	// had it set a DeadLettered condition on the CR named by a dead-lettered
	// message's Clustarr-Key header -- on any kind, in any of the six API groups,
	// which would make one projector a second status writer on every
	// resource in the system and break CLAUDE.md's first invariant, one
	// controller-writer per resource. Ruling R1
	// (docs/superpowers/plans/2026-09-23-phase-g-parity.md) replaces that
	// condition with a metadata annotation,
	// `clustarr.io/dead-lettered: <subject>@<RFC3339>`, applied under this
	// manager, plus an events.k8s.io Event: metadata is not status, and an
	// SSA apply of one annotation key owns exactly that leaf regardless of
	// which kind the object is or which other manager owns the rest of its
	// metadata and spec. Folding the annotation into a condition is left to
	// each owning controller, not built here, so this manager never applies
	// to a status subresource, on any kind -- a guard test asserts that
	// directly rather than trusting the doc comment.
	ManagerDLQProjector FieldManager = "clustarr-dlq-projector"

	// ManagerUI is ui/actions' field manager -- per ruling R2, the only
	// package in ui/ allowed to write anything, and the manager for every
	// write it makes: "search now" creating a Search, "rescan" creating a
	// LibraryScan, "monitor this" patching a catalog kind's
	// spec.monitored. It exists so a metadata.managedFields audit can tell
	// a UI edit from a controller's on the same object, which matters more
	// here than it does for any other manager in this file: every other
	// manager here asserts a controller's or worker's own reconciled truth,
	// but a ui/actions write is a human's one-off intent landing on a spec
	// field some controller (ManagerCatalogarr, or ManagerCatalogarrFanout
	// on a fanned-out child) also reconciles -- and PatchStatus/Apply force
	// ownership unconditionally, so whichever side applies next simply
	// takes the field with no conflict raised either way. The distinct name
	// is what lets an operator or a test tell which side made a given
	// change; it does not by itself prevent one side from overwriting the
	// other.
	//
	// It must never appear on a status subresource. Amendment §A3 and task
	// D3-4 already required the UI to never write status at all; R2 narrows
	// the AST guard (write calls allowed only inside ui/actions, Status()
	// banned everywhere in ui/) and the role guard (create on searches and
	// libraryscans, patch on the catalog kinds' main resource, still no
	// */status and no delete) that enforced that, rather than lifting them.
	// D3-5's e2e assertion that no UI manager appears on any status path
	// must still hold, and a guard test asserts it the same way one does
	// for ManagerDLQProjector above.
	ManagerUI FieldManager = "clustarr-ui"
)

// FieldManagers lists every manager name §2 allows, in spec order.
func FieldManagers() []FieldManager {
	return []FieldManager{
		ManagerCatalogarr,
		ManagerCatalogarrSeries,
		ManagerCatalogarrWorker,
		ManagerCatalogarrMetadata,
		ManagerCatalogarrGrab,
		ManagerCatalogarrFanout,
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
		ManagerDLQProjector,
		ManagerUI,
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
