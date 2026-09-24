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

package download

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DirectGrabReconciler counts the grabs rpc.indexarr.download never sees.
//
// A Download whose DownloadSource is torrentURL, magnetURL or nzbURL is
// fetched by grabarr directly -- it needs no indexer credentials -- so it
// never calls the download verb, which is where every other grab is counted.
// Before this, those grabs went uncounted and status.grabsInWindow
// under-reported every public indexer, which is exactly where a grab limit
// matters least to the tracker but most to an operator who set one.
//
// It watches Download CREATION, because a Download is the grab: catalogarr
// creates one per grab decision and its source is immutable. Each is counted
// into the Indexer its spec.release.indexerRef names, in the Download's own
// namespace, at its creation time, through the same idempotent ring the verb
// uses (CountGrabAt, keyed by GUID), so a restart that meets every existing
// Download again counts nothing twice, and a grab older than the window is
// not counted at all.
//
// The status write is the verb's: countGrabAt re-reads the Indexer and
// applies the complete k8s.ManagerIndexarrWorker set through
// indexarr/status.Patch. A ring or apply failure is returned, so the
// controller retries with backoff rather than losing the grab -- the verb
// swallows the same error because its caller already has the bytes.
type DirectGrabReconciler struct {
	// Client reads Downloads and Indexers and writes Indexer status.
	Client client.Client

	// Bus carries the grab ring in clustarr-indexer-limits. A nil Bus
	// disables accounting, as it does for the verb.
	Bus events.Bus

	// Now is the clock. nil means time.Now.
	Now func() time.Time
}

// IsDirectGrab reports whether a Download's payload is fetched without
// rpc.indexarr.download: every source but indexerDownload.
func IsDirectGrab(src downloadv1alpha1.DownloadSource) bool {
	if src.IndexerDownload != nil {
		return false
	}
	return ptr.Deref(src.TorrentURL, "") != "" ||
		ptr.Deref(src.MagnetURL, "") != "" ||
		ptr.Deref(src.NZBURL, "") != ""
}

func (r *DirectGrabReconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "indexarr.download.DirectGrab.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("download", req.NamespacedName)

	var dl downloadv1alpha1.Download
	if err := r.Client.Get(ctx, req.NamespacedName, &dl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !IsDirectGrab(dl.Spec.Source) {
		return ctrl.Result{}, nil
	}
	ref, guid := dl.Spec.Release.IndexerRef, dl.Spec.Release.GUID
	if ref == "" || guid == "" {
		// A grab from no named indexer counts against none.
		return ctrl.Result{}, nil
	}

	var idx indexv1alpha1.Indexer
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: dl.Namespace, Name: ref}, &idx); err != nil {
		if apierrors.IsNotFound(err) {
			log.Debug("indexarr/download: a direct grab names an indexer that does not exist", "indexer", ref)
			return ctrl.Result{}, nil
		}
		tracing.RecordError(span, err)
		return ctrl.Result{}, err
	}

	s := &Service{Client: r.Client, Bus: r.Bus, Now: r.Now}
	if err := s.countGrabAt(ctx, &idx, guid, dl.CreationTimestamp.Time, s.now(),
		log.With("indexer", idx.Name)); err != nil {
		tracing.RecordError(span, err)
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// directGrabCreated passes the creation of a direct-source Download and
// nothing else. The source is immutable, so an update cannot turn a Download
// into a grab it was not, and the informer's initial list replays every
// existing Download as a create -- which the ring's GUID key makes harmless.
func directGrabCreated() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			dl, ok := e.Object.(*downloadv1alpha1.Download)
			return ok && IsDirectGrab(dl.Spec.Source)
		},
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// SetupWithManager registers the direct-grab counter. indexarr/run.go's
// setupControllers calls it. The name is indexarr's own: grabarr's Download
// controller is "download", and `clustarr all` runs both in one manager,
// where controller names must be unique.
func (r *DirectGrabReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("indexarr-directgrab").
		For(&downloadv1alpha1.Download{}, builder.WithPredicates(directGrabCreated())).
		WithOptions(controller.Options{
			ReconciliationTimeout: time.Minute,
			RecoverPanic:          ptr.To(true),
		}).
		Complete(r)
}
