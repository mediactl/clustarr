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
	// ManagerCatalog is the catalog controller manager. It owns phase
	// and conditions on every catalog.clustarr.io kind.
	ManagerCatalog FieldManager = "catalog"

	// ManagerCatalogSeries is the catalog Series reconciler when it
	// writes to an Episode it owns. Its field set is exactly
	// EpisodeStatus' Title, Overview, AirDate, TvdbID, RuntimeMinutes and
	// AbsoluteNumber -- and nothing else. Not FinaleType, which this
	// comment used to list and which no writer sends (DesiredEpisode does
	// not even carry it); not SceneNumbering, which is M6 work; and not
	// spec.monitored, which ensureEpisode sets once, inside the Create,
	// under the client's own manager rather than this one.
	//
	// It is deliberately distinct from
	// ManagerCatalog, which the Episode reconciler uses for that Episode's own
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
	ManagerCatalogSeries FieldManager = "catalog-series"

	// ManagerCatalogWorker is the catalog queue worker. It covers the
	// consumers that write status fields NO other catalog writer touches:
	// the search worker's Search.status.finishedAt/indexerOutcomes/results.
	// (The import and importlist consumers this comment also named went to
	// import with amendment §A1; neither ever had a subscriber here, and
	// both are gone from pkg/events -- catalog-importlist in the gap-fix
	// wave (X1), catalog-import in gap fixes Z2.)
	//
	// The two consumers that used to share it and could not -- the metadata
	// gateway and the grab path -- have their own names below. See
	// ManagerCatalogGrab for what went wrong.
	ManagerCatalogWorker FieldManager = "catalog-worker"

	// ManagerCatalogMetadata is the catalog metadata gateway. On Movie
	// and Series it applies status.metadata and nothing else.
	//
	// It is deliberately distinct from ManagerCatalogGrab, which applies
	// status.pendingGrab/lastSearchedAt/searchAttempts on the same objects. Server-side apply replaces a manager's whole
	// ownership set on every apply rather than merging it, so while both
	// wrote as catalog-worker each one's apply RELEASED the other's
	// fields: a grab deleted the movie's cached metadata (dropping it to
	// Phase=Pending and forcing a provider refetch), and a metadata refresh
	// deleted status.activeDownloadRef and status.pendingGrab (dropping a
	// delayed item out of Phase=Delayed back to Wanted, where the wanted
	// cron re-searched an item that already had a grab scheduled). Both
	// directions were reproduced against a real apiserver.
	ManagerCatalogMetadata FieldManager = "catalog-metadata"

	// ManagerCatalogGrab is the remediation loop's grab and search stages
	// (ADR-0019 §6.6, §7.3; A4.1, A4.2), one complete set per item pass,
	// written only from cmd/manager: status.pendingGrab (pendingGrabs on a
	// Series or Comic), status.searchDispatch, status.lastSearchedAt,
	// status.searchAttempts and status.donorSearchAttempts. The two stages
	// contribute to one set, and a pass in which either failed applies none
	// of it (remediation.SetDeclarer), so neither releases the other's
	// fields. The grab worker, the RSS matcher's grab and the search worker's
	// attempt writes that used to apply it retired.
	//
	// It never applies status.phase: the item kinds own phase and
	// conditions under ManagerCatalog, and recompute Phase=Delayed from the
	// pendingGrab this manager writes.
	//
	// See ManagerCatalogMetadata for why this is not
	// ManagerCatalogWorker.
	ManagerCatalogGrab FieldManager = "catalog-grab"

	// ManagerCatalogFanout is the Comic reconciler when it writes an
	// Issue's provider-sourced status fields (sourceID, title, date) onto the
	// Issues it owns -- the role ManagerCatalogSeries plays for
	// Series->Episode.
	//
	// Comic->Issue is the only non-video pair that uses it. It was declared
	// (task G1-0) for Artist->Album and Author->Book as well, but G2-1
	// (commit f665aa9) settled that neither of those parents writes onto its
	// children at all: Album and Book are metadata targets of their own, so
	// the gateway fills their status.metadata under ManagerCatalogMetadata
	// from the same provider call a fan-out write would copy, and a second
	// writer of identical values is the co-ownership trap that hides an SSA
	// release. The Artist and Author reconcilers create their children with
	// spec fields only and never apply to them again (their doc.go files say
	// why). Issue, like Episode, has no status.metadata of its own, so its
	// provider fields have nowhere else to come from.
	//
	// It is deliberately distinct from ManagerCatalog, which the Issue
	// reconciler uses for that Issue's own status (state, conditions,
	// hasFile, fileRef, fileQuality, cutoffMet, activeDownloadRef), exactly as
	// Episode's reconciler uses ManagerCatalog for its own
	// Phase/Conditions/HasFile rather than ManagerCatalogSeries.
	// Server-side apply replaces a manager's whole ownership set on every
	// apply, so a parent and its child sharing one manager name on the child
	// object would silently release each other's fields the next time
	// either side reconciled -- proven once already for Series/Episode (see
	// ManagerCatalogSeries).
	ManagerCatalogFanout FieldManager = "catalog-fanout"

	// ManagerCatalogArtwork is the renderer role (catalog --role
	// artwork, spec §C.6, §B.6). On Movie and Series it applies
	// status.overlay and nothing else, and writes only "overlay" objects in
	// the artwork bucket -- it never touches "original" objects or
	// status.artwork, which belong to ManagerCatalogMetadata, the
	// metadata gateway (spec §B.3): two writers split by variant, the same
	// discipline as the MediaFile spec/status split. Distinct from every
	// other catalog manager for the same reason ManagerCatalogMetadata
	// and ManagerCatalogGrab are: server-side apply replaces a manager's
	// whole ownership set on every apply, so a shared name would let one
	// role's apply silently release the other's fields.
	ManagerCatalogArtwork FieldManager = "catalog-artwork"

	// ManagerCatalogMarkers is RETIRED (loop spec 2026-10-06 §2.12,
	// §3.14): it was the metadata gateway's marker worker, the writer of
	// MediaFile status.markers until the remediation loop took every status
	// leaf under ManagerCatalog. No writer may use it; Validate still
	// accepts it (RetiredFieldManagers) because release N's one release
	// apply of every MediaFile's catalog-markers entry runs under it
	// (§7.3.8, F8.4). F9.4 removes it.
	ManagerCatalogMarkers FieldManager = "catalog-markers"

	// ManagerCatalogClassify is the Series reconciler's one-time anime
	// classification: it merge-patches spec.seriesType and
	// spec.qualityProfileRef once, never again for that Series. Distinct
	// from every creator's manager (import, import-worker,
	// clustarr-ui), so the patch reads in managedFields as the classifier's.
	ManagerCatalogClassify FieldManager = "catalog-classify"

	// ManagerImport is the import controller manager. It owns ImportList,
	// ImportExclusion and LibraryScan status.
	ManagerImport FieldManager = "import"

	// ManagerImportWorker is an import scan, list or file-import worker.
	// On MediaFile it applies MediaFileSpec only -- what it observed on disk,
	// plus the values frozen at import -- and never MediaFileStatus, which
	// catalog owns in full. The library-rescan worker also applies
	// MovieSpec under this name when it creates the Movie a scanned file is
	// attributed to. (This comment previously described a
	// status.file/status.probe split; those fields do not exist. See
	// CLAUDE.md's invariant.)
	//
	// It is deliberately distinct from ManagerImport even though the
	// workers run inside the same manager process as import's
	// controllers -- see rescan.FieldManager, which is the constant
	// production writes with and where the reasoning lives. Until task C14
	// the rescan worker actually wrote as ManagerImport while this
	// comment, catalog's two-writer gate and the e2e suite each said
	// something different.
	ManagerImportWorker FieldManager = "import-worker"

	// ManagerIndex is the index controller manager. On Indexer it owns
	// the configuration half of status: conditions, protocol, privacy, caps,
	// observedGeneration and sessionSecretRef -- plus queriesInWindow and
	// grabsInWindow, its projection of the clustarr-indexer-limits rings
	// (since 2026-10-01). IndexerDefinition and
	// IndexerProxy have one writer each, so it owns those outright.
	//
	// Since the manager/agent split (2026-10-06) it is written only by the
	// manager: the three reconcilers, the bundle loader and the Indexer
	// reconciler's session-Secret Saves.
	ManagerIndex FieldManager = "index"

	// ManagerIndexWorker is index's RSS poll and search fan-out. On
	// Indexer it owns the observed half of status: lastRssAt, lastRssNewCount,
	// indexedReleases and the escalation fields (failureLevel,
	// initialFailureAt, disabledUntil, lastFailureAt, lastFailureMsg), each
	// applied as a compare-and-swap (app/indexer/status.PatchCAS).
	//
	// It is deliberately distinct from ManagerIndex because Indexer.status
	// has three writer paths -- the reconciler, the RSS poll and the search
	// fan-out -- which is one more than the design spec anticipated. Server-
	// side apply replaces a manager's whole ownership set on every apply, so
	// two paths sharing one manager name silently release each other's
	// fields; that hazard took eight distinct forms in Phase C and the
	// remedy that worked, twice, was distinct managers. The two worker paths
	// DO share this name, so both declare the identical set through
	// app/indexer/status.WorkerFields -- one definition, not two.
	//
	// In the index agent it also writes the owned session Secret (a cached
	// client's relogin Save and the compare-and-swap Drop, through the same
	// complete declaration as the manager's Save) and creates the facade's
	// API-key Secret (§13 OD14).
	ManagerIndexWorker FieldManager = "index-worker"

	// ManagerGrab is the grab controller manager, the single writer of
	// DownloadClient status and the engine workloads it renders.
	ManagerGrab FieldManager = "grab"

	// ManagerTranscode is the transcode controller manager.
	ManagerTranscode FieldManager = "transcode"

	// ManagerTranscodePool is the transcode controller's pool Jobs: the sole
	// writer of their spec, including spec.scheduling.schedulingPolicy.gang.minCount.
	ManagerTranscodePool FieldManager = "transcode-pool"

	// ManagerCaption is the caption controller manager.
	ManagerCaption FieldManager = "caption"

	// ManagerCaptionWorker is a caption subtitle fetch worker. It
	// applies SubtitleRequest.status.items entries only.
	ManagerCaptionWorker FieldManager = "caption-worker"

	// ManagerDLQProjector is the DLQ projector wired into catalog's
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
	// field some controller (ManagerCatalog, or ManagerCatalogFanout
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

	// ManagerAutoscale is the manager's autoscale reconciler and external-
	// metrics CA injector (spec 2026-10-06 §5.3.6, §9.4, §9.5). It applies
	// each autoscaled agent Deployment's HorizontalPodAutoscaler, writes a
	// Deployment's scale subresource to wake one at zero that its HPA did
	// not scale there, and owns only spec.caBundle on the
	// v1beta1.external.metrics.k8s.io APIService. Written only from
	// cmd/manager; never on a status subresource.
	ManagerAutoscale FieldManager = "clustarr-autoscale"
)

// DefaultFieldOwner is the field manager controller-runtime records for a
// write that names none (spec 2026-10-06 §5.3.5, OD4). Without it the
// binaries' names would appear ("manager", "agent"), leaving stale entries
// and an unreadable audit across the split. It is untyped on purpose: it is
// not a FieldManager, so Validate never accepts it for server-side apply,
// and every PatchStatus, Apply and client.FieldOwner site keeps its own name.
// It is also the item finalizer's field owner: EnsureFinalizer and
// RemoveFinalizer write catalogv1alpha1.FinalizerTransfers
// (download.clustarr.io/transfers) on an owning item under it, from
// cmd/manager only (ADR-0019 §6.8).
const DefaultFieldOwner = "clustarr"

// FieldManagers lists every manager name §2 allows, in spec order.
func FieldManagers() []FieldManager {
	return []FieldManager{
		ManagerCatalog,
		ManagerCatalogSeries,
		ManagerCatalogWorker,
		ManagerCatalogMetadata,
		ManagerCatalogGrab,
		ManagerCatalogFanout,
		ManagerCatalogArtwork,
		ManagerCatalogClassify,
		ManagerImport,
		ManagerImportWorker,
		ManagerIndex,
		ManagerIndexWorker,
		ManagerGrab,
		ManagerTranscode,
		ManagerTranscodePool,
		ManagerCaption,
		ManagerCaptionWorker,
		ManagerDLQProjector,
		ManagerUI,
		ManagerAutoscale,
	}
}

// RetiredFieldManagers are names no writer may use any more but Validate
// still accepts: release N's one release apply of every MediaFile's
// catalog-markers entry runs under it (loop spec §7.3.8, F8.4). F9.4 empties
// the list. grab-engine went with the Download kind (ADR-0019 A9.1).
func RetiredFieldManagers() []FieldManager {
	return []FieldManager{ManagerCatalogMarkers}
}

// String returns the manager name as the apiserver sees it.
func (f FieldManager) String() string { return string(f) }

// Valid reports whether f is one of the names §2 enumerates, live or
// retired.
func (f FieldManager) Valid() bool {
	for _, known := range append(FieldManagers(), RetiredFieldManagers()...) {
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
