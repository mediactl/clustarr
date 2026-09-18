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
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// chooseSource picks the DownloadSource variant for a release, honouring the
// CRD's "exactly one of magnetURL, torrentURL, nzbURL or indexerDownload"
// rule.
//
// The order is magnet, then a direct URL when the indexer needs no
// credentials, then indexerDownload. §8.2's parenthetical is "source
// (indexerDownload when the indexer is authenticated)": a .torrent or .nzb URL
// from an authenticated indexer is useless to a download engine, which holds
// no indexer session, so it must be resolved through indexarr instead.
//
// indexerRequiresAuth is deliberately fail-safe at every call site: an Indexer
// that cannot be read is treated as authenticated, which routes the grab
// through indexarr rather than handing an engine a URL that will 401.
//
// ExpectedInfoHash is set whenever the release carries one, on every branch.
// It is the guard that stops an indexer swapping content out from under a
// decision, and it is orthogonal to how the payload is addressed.
func chooseSource(release commonv1.ReleaseInfo, indexerRequiresAuth bool) *downloadac.DownloadSourceApplyConfiguration {
	src := downloadac.DownloadSource()
	switch {
	case release.MagnetURL != "":
		src = src.WithMagnetURL(release.MagnetURL)
	case !indexerRequiresAuth && release.DownloadURL != "" && release.Protocol == commonv1.ProtocolTorrent:
		src = src.WithTorrentURL(release.DownloadURL)
	case !indexerRequiresAuth && release.DownloadURL != "" && release.Protocol == commonv1.ProtocolUsenet:
		src = src.WithNZBURL(release.DownloadURL)
	default:
		src = src.WithIndexerDownload(downloadac.IndexerDownload().
			WithIndexerRef(release.IndexerRef).
			WithGUID(release.GUID).
			WithURL(release.DownloadURL))
	}
	if release.Protocol == commonv1.ProtocolTorrent && release.InfoHash != "" {
		src = src.WithExpectedInfoHash(release.InfoHash)
	}
	return src
}

// performGrab is spec §8.2's grab step, in order: take every lease
// all-or-nothing, re-read each item's activeDownloadRef under an optimistic
// lock, create the deterministically named Download with an ownerRef, set
// activeDownloadRef, publish release.grabbed.
//
// It returns ErrDuplicateGrab -- which callers acknowledge rather than retry
// -- from both guards: the lease (another worker got here first) and the
// re-read (a path that does not go through the lease at all, such as a Search
// CR's manual grab, already claimed the item). Both of those exits clear
// status.pendingGrab first: see clearPendingGrab for why an ack that leaves it
// set takes the item out of automation permanently.
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
	downloadName := k8s.ChildName(target.Name, release.GUID)
	kv := d.Bus.KV(events.BucketLeases)

	acquired, err := acquireLeases(ctx, kv, leaseKeys(ns, statusTargets), downloadName)
	if err != nil {
		if errors.Is(err, ErrDuplicateGrab) {
			metrics.SearchDecisionsTotal.WithLabelValues(string(target.Kind), "duplicate", "leaseHeld").Inc()
			return clearPendingGrab(ctx, d, ns, statusTargets, err)
		}
		return err
	}

	// From here on, every failure exit must release the leases it took:
	// a lease left behind by a grab that never created a Download blocks
	// the item for the ten minutes it takes the sweeper to notice.
	ops := make([]kindOps, len(statusTargets))
	statuses := make([]workerStatus, len(statusTargets))
	var firstObj client.Object
	for i, st := range statusTargets {
		o, opErr := kindOpsFor(st.Kind)
		if opErr != nil {
			releaseLeases(ctx, kv, acquired)
			return opErr
		}
		ops[i] = o

		obj, getErr := o.get(ctx, d.Client, ns, st.Name)
		if getErr != nil {
			releaseLeases(ctx, kv, acquired)
			if apierrors.IsNotFound(getErr) {
				return events.Discard("grab: status target no longer exists", getErr)
			}
			return fmt.Errorf("grab: re-read %s/%s: %w", st.Kind, st.Name, getErr)
		}
		if i == 0 {
			firstObj = obj
		}
		ws := o.workerStatus(obj)
		if ws.ActiveDownloadRef != nil && *ws.ActiveDownloadRef != downloadName {
			// The optimistic-lock half of the double-grab guard: the lease
			// was free, but something that does not take leases (a Search
			// CR's manual grab) already put a Download on this item.
			releaseLeases(ctx, kv, acquired)
			metrics.SearchDecisionsTotal.WithLabelValues(string(target.Kind), "duplicate", "activeDownload").Inc()
			return clearPendingGrab(ctx, d, ns, statusTargets,
				fmt.Errorf("%w: %s/%s already has download %q", ErrDuplicateGrab, st.Kind, st.Name, *ws.ActiveDownloadRef))
		}
		statuses[i] = ws
	}

	owner, err := getTarget(ctx, d.Client, ns, target)
	if err != nil {
		releaseLeases(ctx, kv, acquired)
		if apierrors.IsNotFound(err) {
			return events.Discard("grab: target no longer exists", err)
		}
		return fmt.Errorf("grab: get target %s/%s: %w", target.Kind, target.Name, err)
	}
	ownerRef, err := k8s.OwnerReferenceAC(owner, d.Client.Scheme())
	if err != nil {
		releaseLeases(ctx, kv, acquired)
		return fmt.Errorf("grab: owner reference for %s/%s: %w", target.Kind, target.Name, err)
	}

	// The grab configuration comes from the first status target: a movie's
	// own spec, or -- for a single episode and for every episode of a pack --
	// the owning Series, which every episode of one pack shares.
	gctx, err := ops[0].grabContext(ctx, d.Client, firstObj)
	if err != nil {
		releaseLeases(ctx, kv, acquired)
		return err
	}

	indexer, indexerErr := getIndexer(ctx, d.Client, ns, release.IndexerRef)
	if indexerErr != nil && !apierrors.IsNotFound(indexerErr) {
		releaseLeases(ctx, kv, acquired)
		return fmt.Errorf("grab: get indexer %q: %w", release.IndexerRef, indexerErr)
	}
	// A missing Indexer is treated as authenticated rather than as an error:
	// routing through indexarr is the safe branch, and failing the whole grab
	// because an Indexer object was renamed would be worse than grabbing it
	// the slow way.
	requiresAuth := indexer == nil || indexer.Spec.SecretRef != nil

	spec := downloadac.DownloadSpec().
		WithProtocol(release.Protocol).
		WithSource(chooseSource(release, requiresAuth)).
		WithRelease(release).
		WithTarget(commonv1.MediaRef{Kind: target.Kind, Name: target.Name, Keys: keys}).
		WithGrabbedBy(grabbedBy)
	if gctx.QualityProfileRef != "" {
		spec = spec.WithQualityProfileRef(gctx.QualityProfileRef)
	}
	// SeedCriteria comes from the Indexer when it sets one -- indexer_types.go
	// says it "overrides the download client's seeding limits" -- and is left
	// unset otherwise so the DownloadClient's own default applies.
	if indexer != nil && indexer.Spec.SeedCriteria != nil {
		spec = spec.WithSeedCriteria(*indexer.Spec.SeedCriteria)
	}

	dl := downloadac.Download(downloadName, ns).
		WithOwnerReferences(ownerRef).
		WithSpec(spec)
	if _, err := k8s.Apply(ctx, d.Client, k8s.ManagerCatalogarrGrab, dl); err != nil {
		releaseLeases(ctx, kv, acquired)
		return fmt.Errorf("grab: create download %q: %w", downloadName, err)
	}

	// Past this point the leases are NOT released on failure. The Download
	// exists; releasing the lease would let a redelivery grab the same item a
	// second time, and the grab is idempotent from here (server-side apply on
	// a deterministic name, then a status apply) so a retry converges.
	for i, st := range statusTargets {
		ws := statuses[i]
		ws.ActiveDownloadRef = &downloadName
		// The pending candidate has been consumed: leaving it set would keep
		// the item at Phase=Delayed for the whole seven-day bucket TTL even
		// though its Download is already running.
		ws.PendingGrab = nil
		if err := ops[i].applyWorkerStatus(ctx, d.Client, ns, st.Name, ws); err != nil {
			return fmt.Errorf("grab: set activeDownloadRef on %s/%s: %w", st.Kind, st.Name, err)
		}
	}

	metrics.SearchDecisionsTotal.WithLabelValues(string(target.Kind), "grabbed", string(grabbedBy)).Inc()
	return publishGrabbed(ctx, d, ns, target, owner, downloadName, release)
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
// Every apply carries the whole k8s.ManagerCatalogarrGrab-owned set, read
// fresh, so a retry cannot release anything either.
func clearPendingGrab(ctx context.Context, d Deps, ns string, statusTargets []commonv1.MediaRef, dup error) error {
	for _, st := range statusTargets {
		ops, err := kindOpsFor(st.Kind)
		if err != nil {
			return err
		}
		obj, err := ops.get(ctx, d.Client, ns, st.Name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				// Nothing to strand.
				continue
			}
			return fmt.Errorf("grab: read %s/%s to clear pendingGrab: %w", st.Kind, st.Name, err)
		}
		ws := ops.workerStatus(obj)
		if ws.PendingGrab == nil {
			continue
		}
		ws.PendingGrab = nil
		if err := ops.applyWorkerStatus(ctx, d.Client, ns, st.Name, ws); err != nil {
			return fmt.Errorf("grab: clear pendingGrab on %s/%s after a duplicate grab: %w", st.Kind, st.Name, err)
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
