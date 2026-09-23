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

package grab

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
	"github.com/mediactl/clustarr/catalogarr/worker/grab/downloads"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// Approved is a release that has already passed pkg/decision.Evaluate and won
// pkg/decision.Rank for one catalog item. It is what a caller hands Decide.
type Approved struct {
	// Namespace is the namespace of every object involved: the target, the
	// Indexer, the Download.
	Namespace string

	// Target is the catalog item the release is for, or the Series parent of
	// a pack. It becomes the Download's ownerReference and spec.target.
	Target commonv1.MediaRef

	// Keys narrows a pack to the Episode names it covers. It is empty for a
	// movie or a single episode and required for a Series target.
	Keys []string

	// Release is the winning release.
	Release commonv1.ReleaseInfo

	// GrabbedBy records what caused the grab, verbatim onto the Download.
	GrabbedBy downloadv1alpha1.GrabSource
}

// Deps is everything this package needs from the process around it. Now is a
// seam for tests; nil means time.Now.
type Deps struct {
	Client client.Client
	Bus    events.Bus
	Now    func() time.Time

	// Reader is an uncached reader -- manager.GetAPIReader() -- for the two
	// reads that decide whether a grab is a duplicate: the double-grab
	// guard's Download list and the lease holder's Download get. Nil falls
	// back to Client.
	//
	// Client reads through the manager's informer cache, which lags the
	// apiserver. A Search CR's interactive grab takes no lease, so the
	// Download list is the only thing that can see it, and a Download
	// created a moment before this grab may not have reached the cache yet:
	// a different release would then be grabbed a second time, beside it.
	// Grabs are rare enough that a live List per grab costs nothing worth
	// that window.
	Reader client.Reader
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// liveReader is Reader when the process supplied one, Client otherwise.
func (d Deps) liveReader() client.Reader {
	if d.Reader != nil {
		return d.Reader
	}
	return d.Client
}

// performGrab is spec §8.2's grab step, in order: take every lease
// all-or-nothing, look up the Downloads already working on each item, create
// the deterministically named Download with an ownerRef, clear the consumed
// pendingGrab, publish release.grabbed.
//
// It returns ErrDuplicateGrab -- which callers acknowledge rather than retry
// -- from both guards: the lease (another automatic grab holds the item) and
// the Download lookup (a path that takes no lease, such as a Search CR's
// interactive grab, already put a Download on the item). Both of those exits
// clear status.pendingGrab first: see clearPendingGrab for why an ack that
// leaves it set takes the item out of automation permanently.
//
// It does not write status.activeDownloadRef. Gap-fix ruling R-5 gives that
// field one writer, the item's reconciler, which derives it from the item's
// non-terminal Download -- the Download this creates.
func performGrab(
	ctx context.Context,
	d Deps,
	ns string,
	target commonv1.MediaRef,
	keys []string,
	release commonv1.ReleaseInfo,
	grabbedBy downloadv1alpha1.GrabSource,
) error {
	ctx, span := tracing.Start(ctx, "grab.performGrab")
	defer span.End()

	statusTargets, err := StatusTargets(target, keys)
	if err != nil {
		return err
	}
	source, err := downloads.ResolveSource(release)
	if err != nil {
		// The release snapshot is immutable, so no retry can give it a
		// source. Clear pendingGrab before giving up, or the discard strands
		// the item at Phase=Delayed exactly as a dead letter would.
		return clearPendingGrab(ctx, d, ns, statusTargets,
			events.Discard("grab: release has nothing to download it by", err))
	}
	downloadName := k8s.ChildName(target.Name, release.GUID)
	kv := d.Bus.KV(events.BucketLeases)

	acquired, err := acquireLeases(ctx, kv, leaseKeys(ns, statusTargets), downloadName, d.leaseHolder(ns))
	if err != nil {
		if errors.Is(err, ErrDuplicateGrab) {
			metrics.SearchDecisionsTotal.WithLabelValues(string(target.Kind), "duplicate", "leaseHeld").Inc()
			return clearPendingGrab(ctx, d, ns, statusTargets, err)
		}
		return err
	}

	// From here on, every failure exit before the Download exists must
	// release the leases it took: a lease left behind by a grab that never
	// created a Download blocks the item until leaseOrphanGrace passes.
	items := make([]client.Object, len(statusTargets))
	for i, st := range statusTargets {
		o, opErr := kindOpsFor(st.Kind)
		if opErr != nil {
			releaseLeases(ctx, kv, acquired)
			return opErr
		}
		obj, getErr := o.get(ctx, d.Client, ns, st.Name)
		if getErr != nil {
			releaseLeases(ctx, kv, acquired)
			if apierrors.IsNotFound(getErr) {
				return events.Discard("grab: status target no longer exists", getErr)
			}
			return fmt.Errorf("grab: re-read %s/%s: %w", st.Kind, st.Name, getErr)
		}
		items[i] = obj
	}

	resume, err := guardExistingDownloads(ctx, d.liveReader(), ns, downloadName, statusTargets, items)
	if err != nil {
		releaseLeases(ctx, kv, acquired)
		if errors.Is(err, ErrDuplicateGrab) {
			metrics.SearchDecisionsTotal.WithLabelValues(string(target.Kind), "duplicate", "activeDownload").Inc()
			return clearPendingGrab(ctx, d, ns, statusTargets, err)
		}
		return err
	}

	owner, err := getTarget(ctx, d.Client, ns, target)
	if err != nil {
		releaseLeases(ctx, kv, acquired)
		if apierrors.IsNotFound(err) {
			return events.Discard("grab: target no longer exists", err)
		}
		return fmt.Errorf("grab: get target %s/%s: %w", target.Kind, target.Name, err)
	}

	if !resume {
		if err := createDownload(ctx, d, ns, downloadName, owner, target, keys, release, source, grabbedBy, statusTargets, items); err != nil {
			releaseLeases(ctx, kv, acquired)
			return err
		}
	}

	// Past this point the leases are NOT released on failure. The Download
	// exists; releasing the lease would let a redelivery grab the same item a
	// second time. The redelivery instead re-enters its own lease, finds its
	// own Download (resume) and finishes from here.
	for _, st := range statusTargets {
		// The pending candidate has been consumed: leaving it set would keep
		// the item at Phase=Delayed for the whole seven-day bucket TTL even
		// though its Download is already running.
		err := updateWorkerStatus(ctx, d.Client, ns, st, func(ws *workerStatus) bool {
			if ws.PendingGrab == nil {
				return false
			}
			ws.PendingGrab = nil
			return true
		})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("grab: clear pendingGrab on %s/%s: %w", st.Kind, st.Name, err)
		}
	}

	metrics.SearchDecisionsTotal.WithLabelValues(string(target.Kind), "grabbed", string(grabbedBy)).Inc()
	return publishGrabbed(ctx, d, ns, target, owner, downloadName, release)
}

// createDownload applies the grab's Download. Its spec.source comes from
// downloads.ResolveSource, the one mapping the Search controller's
// interactive grabs use as well.
func createDownload(
	ctx context.Context,
	d Deps,
	ns, downloadName string,
	owner client.Object,
	target commonv1.MediaRef,
	keys []string,
	release commonv1.ReleaseInfo,
	source downloadv1alpha1.DownloadSource,
	grabbedBy downloadv1alpha1.GrabSource,
	statusTargets []commonv1.MediaRef,
	items []client.Object,
) error {
	ownerRef, err := k8s.OwnerReferenceAC(owner, d.Client.Scheme())
	if err != nil {
		return fmt.Errorf("grab: owner reference for %s/%s: %w", target.Kind, target.Name, err)
	}

	// The grab configuration comes from the first status target: a movie's
	// own spec, or -- for a single episode and for every episode of a pack --
	// the owning Series, which every episode of one pack shares.
	ops, err := kindOpsFor(statusTargets[0].Kind)
	if err != nil {
		return err
	}
	gctx, err := ops.grabContext(ctx, d.Client, items[0])
	if err != nil {
		return err
	}

	indexer, err := getIndexer(ctx, d.Client, ns, release.IndexerRef)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("grab: get indexer %q: %w", release.IndexerRef, err)
	}

	spec := downloadac.DownloadSpec().
		WithProtocol(release.Protocol).
		WithSource(downloads.SourceApplyConfiguration(source)).
		WithRelease(release).
		WithTarget(commonv1.MediaRef{Kind: target.Kind, Name: target.Name, Keys: keys}).
		WithGrabbedBy(grabbedBy)
	if gctx.QualityProfileRef != "" {
		spec = spec.WithQualityProfileRef(gctx.QualityProfileRef)
	}
	// SeedCriteria comes from the Indexer when it sets one -- indexer_types.go
	// says it "overrides the download client's seeding limits" -- and is left
	// unset otherwise so the DownloadClient's own default applies. A missing
	// Indexer only loses that override; it does not stop the grab.
	if indexer != nil && indexer.Spec.SeedCriteria != nil {
		spec = spec.WithSeedCriteria(*indexer.Spec.SeedCriteria)
	}

	dl := downloadac.Download(downloadName, ns).
		WithOwnerReferences(ownerRef).
		WithSpec(spec)
	if _, err := k8s.Apply(ctx, d.Client, k8s.ManagerCatalogarrGrab, dl); err != nil {
		return fmt.Errorf("grab: create download %q: %w", downloadName, err)
	}
	return nil
}

// guardExistingDownloads is the half of the double-grab guard that no lease
// can provide: it asks the apiserver which Downloads are already working on
// the grab's items -- downloads.Covers, and rollup.DownloadNonTerminal, the
// same liveness test the reconcilers derive status.activeDownloadRef from.
// c should be Deps.Reader, a live read: see its doc for the cache window a
// cached list leaves open.
//
// It replaces a re-read of status.activeDownloadRef that could never fire:
// nothing on the interactive path set that ref, and under ruling R-5 this
// package no longer reads or writes it. A lease stops two automatic grabs; a
// Search CR's spec.grab takes no lease, so only the Downloads themselves show
// that it got there first.
//
// resume is true when the one Download that exists is this grab's own -- the
// deterministic name, applied by k8s.ManagerCatalogarrGrab, still active --
// left by an earlier delivery that failed after creating it. The caller then
// skips the apply and finishes the grab.
//
// Everything else that covers an item is ErrDuplicateGrab:
//
//   - another active Download, whoever made it;
//   - a Download with this grab's own name that another path applied -- the
//     Search controller's grab of the same release. Re-applying over it is
//     what used to happen, and it could only go wrong: its source, and before
//     that its grabbedBy and manual flag, are that path's, and
//     DownloadSpec.Source is immutable, so the apply was rejected, the task
//     dead-lettered before clearPendingGrab ran, and the item sat at
//     Phase=Delayed for good;
//   - a Download with this grab's own name that is already terminal. The
//     name is deterministic, so the same release cannot be grabbed for the
//     same item twice while the old object exists: applying onto it would
//     report a grab and download nothing.
//
// Terminal Downloads under other names are history, not occupants: a failed
// or imported download does not stop the next grab.
func guardExistingDownloads(
	ctx context.Context,
	c client.Reader,
	ns, downloadName string,
	statusTargets []commonv1.MediaRef,
	items []client.Object,
) (resume bool, err error) {
	var list downloadv1alpha1.DownloadList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return false, fmt.Errorf("grab: list downloads for the double-grab guard: %w", err)
	}
	var blockers []string
	for i := range list.Items {
		dl := &list.Items[i]
		if dl.Name == downloadName {
			if rollup.DownloadNonTerminal(dl) && appliedByGrabPath(dl) {
				resume = true
				continue
			}
			blockers = append(blockers, dl.Name)
			continue
		}
		if !rollup.DownloadNonTerminal(dl) {
			continue
		}
		for j, st := range statusTargets {
			if downloads.Covers(dl, st.Kind, st.Name, items[j].GetUID()) {
				blockers = append(blockers, dl.Name)
				break
			}
		}
	}
	if resume {
		// This grab already happened; whatever else is on the item is a
		// problem for the next decision, not a reason to abandon this one
		// half-finished.
		return true, nil
	}
	if len(blockers) > 0 {
		return false, fmt.Errorf("%w: already has download %v", ErrDuplicateGrab, blockers)
	}
	return false, nil
}

// appliedByGrabPath reports whether this package created dl: the main
// resource carries a field-manager entry for k8s.ManagerCatalogarrGrab. The
// Search controller applies its Downloads as k8s.ManagerCatalogarr, so a
// Download of the same release made by a user's spec.grab is never mistaken
// for this path's own.
func appliedByGrabPath(dl *downloadv1alpha1.Download) bool {
	for _, mf := range dl.ManagedFields {
		if mf.Manager == k8s.ManagerCatalogarrGrab.String() && mf.Subresource == "" {
			return true
		}
	}
	return false
}

// leaseHolder answers acquireLeases' question about a lease someone else
// holds: does the Download it names still occupy the item?
func (d Deps) leaseHolder(ns string) holderFunc {
	return func(ctx context.Context, entry events.Entry) (holderState, error) {
		var dl downloadv1alpha1.Download
		err := d.liveReader().Get(ctx, client.ObjectKey{Namespace: ns, Name: string(entry.Value)}, &dl)
		switch {
		case apierrors.IsNotFound(err):
			// Either a grab that took the lease and has not created its
			// Download yet, or one whose Download is gone. Only age can
			// tell them apart.
			if d.now().Sub(entry.Created) < leaseOrphanGrace {
				return holderActive, nil
			}
			return holderStale, nil
		case err != nil:
			return holderActive, err
		case rollup.DownloadNonTerminal(&dl):
			return holderActive, nil
		default:
			return holderStale, nil
		}
	}
}

// clearPendingGrab drops status.pendingGrab from every status target and then
// returns dup, so the caller still sees ErrDuplicateGrab and acknowledges.
//
// Acknowledging a duplicate without this is how an item leaves automation for
// good. Deleting the clustarr-pending entry -- which the handler does -- says
// nothing about the object: status.pendingGrab stays set, the reconciler keeps
// recomputing Phase=Delayed from it, and wantedcron's sweep only picks up
// Wanted and CutoffUnmet items, so nothing ever searches for it again. There
// is no error anywhere; the item simply stops moving.
//
// A failed clear is NOT wrapped in ErrDuplicateGrab: the caller must retry
// rather than ack, because acking is precisely what strands the item. The
// retry re-runs performGrab, hits the same guard and tries the clear again.
// Every write goes through updateWorkerStatus, so it carries the whole
// k8s.ManagerCatalogarrGrab-owned set, read fresh, and cannot release or roll
// back anything either.
func clearPendingGrab(ctx context.Context, d Deps, ns string, statusTargets []commonv1.MediaRef, dup error) error {
	for _, st := range statusTargets {
		err := updateWorkerStatus(ctx, d.Client, ns, st, func(ws *workerStatus) bool {
			if ws.PendingGrab == nil {
				return false
			}
			ws.PendingGrab = nil
			return true
		})
		switch {
		case apierrors.IsNotFound(err):
			// Nothing to strand.
			continue
		case err != nil:
			return fmt.Errorf("grab: clear pendingGrab on %s/%s: %w", st.Kind, st.Name, err)
		}
	}
	return dup
}

func getIndexer(ctx context.Context, c client.Client, ns, name string) (*indexv1alpha1.Indexer, error) {
	if name == "" {
		return nil, apierrors.NewNotFound(indexv1alpha1.GroupVersion.WithResource("indexers").GroupResource(), name)
	}
	var idx indexv1alpha1.Indexer
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

// publishGrabbed emits evt.catalog.release.grabbed. indexarr consumes it to
// record the grab against clustarr-indexer-limits (§8.3), so a failure here is
// returned: the caller retries, and the deterministic Download name plus the
// held lease make the retry a no-op up to this point.
func publishGrabbed(
	ctx context.Context,
	d Deps,
	ns string,
	target commonv1.MediaRef,
	owner client.Object,
	downloadName string,
	release commonv1.ReleaseInfo,
) error {
	now := d.now()
	evt := schema.ReleaseEvent{
		Media:        commonv1.MediaRef{Kind: target.Kind, Name: target.Name},
		Action:       events.ActionGrabbed,
		GUID:         release.GUID,
		Indexer:      release.IndexerName,
		ReleaseGroup: release.ReleaseGroup,
		DownloadRef:  &schema.Ref{Namespace: ns, Name: downloadName},
		Quality:      release.Quality,
		FormatScore:  release.FormatScore,
		At:           now,
	}
	if release.IndexerRef != "" {
		evt.IndexerRef = &schema.Ref{Namespace: ns, Name: release.IndexerRef}
	}
	schemaName, data, err := schema.Encode(evt)
	if err != nil {
		return err
	}
	env := &events.Envelope{
		ID:     downloadName + ":grabbed",
		Type:   "catalog.ReleaseEvent",
		Schema: schemaName,
		Source: "catalogarr-worker@" + version.String(),
		Key:    ns + "/" + target.Name,
		Time:   now,
		Data:   data,
	}
	tracing.Inject(ctx, env)
	if _, err := d.Bus.Publish(ctx, events.CatalogReleaseSubject(events.ActionGrabbed, string(owner.GetUID())), env); err != nil {
		return fmt.Errorf("grab: publish release.grabbed: %w", err)
	}
	return nil
}
