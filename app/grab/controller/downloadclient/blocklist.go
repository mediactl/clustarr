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

// The +kubebuilder:rbac markers for this package live in doc.go, at package
// level -- see that file for why.
package downloadclient

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// ReasonBlocklistExpired is the Normal event reason recorded when a
// blocklisted Download is deleted.
const ReasonBlocklistExpired = "BlocklistExpired"

// BlocklistSweeper deletes a Download once its blocklist entry expires.
//
// It claims no part of k8s.ManagerGrabarr's Download.status set and never
// calls app/grab/status.Patch: download_types.go's own doc comments on
// LabelBlocklisted and BlocklistedUntil are explicit that grabarr DELETES the
// Download once the deadline passes, not that it clears the field. There is
// therefore no status apply here to build a partial declaration of -- see
// doc.go for the full reasoning and the source lines this reads from.
//
// It is level-driven and time-triggered rather than purely event-driven: a
// Download whose blocklist has not expired yet gets no further watch event
// between being labelled and its deadline, so Reconcile computes
// RequeueAfter from status.blocklistedUntil itself, the same "wake me when
// the deadline arrives" shape a lease or a TTL sweep needs anywhere in this
// tree.
type BlocklistSweeper struct {
	Client   client.Client
	Recorder events.EventRecorder

	// Now is the clock; nil means time.Now. A seam for tests, exactly as
	// app/catalog/worker/grab.Deps.Now is.
	Now func() time.Time
}

// NewBlocklistSweeper builds a BlocklistSweeper with the real clock.
func NewBlocklistSweeper(c client.Client, recorder events.EventRecorder) *BlocklistSweeper {
	return &BlocklistSweeper{Client: c, Recorder: recorder, Now: time.Now}
}

func (s *BlocklistSweeper) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *BlocklistSweeper) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "downloadclient.BlocklistSweeper.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("download", req.NamespacedName)

	var dl downloadv1alpha1.Download
	if err := s.Client.Get(ctx, req.NamespacedName, &dl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// The controller-runtime predicate on SetupWithManager already restricts
	// which objects reach Reconcile at all, but Reconcile re-checks both
	// conditions itself rather than trusting the predicate: a predicate
	// filters watch EVENTS, and this defends the READ, which is what actually
	// decides whether a delete happens.
	if dl.Labels[downloadv1alpha1.LabelBlocklisted] != downloadv1alpha1.LabelBlocklistedValue {
		return ctrl.Result{}, nil
	}
	if dl.Status.BlocklistedUntil == nil {
		// Labelled but no deadline recorded (yet, or ever, if a caller sets
		// the label without the field). Nothing to sweep against; wait for the
		// next event rather than guess a deadline.
		return ctrl.Result{}, nil
	}

	until := dl.Status.BlocklistedUntil.Time
	now := s.now()
	if now.Before(until) {
		return ctrl.Result{RequeueAfter: until.Sub(now)}, nil
	}

	// dl was just read at the top of THIS reconcile, so the delete's
	// precondition is on a fresh resourceVersion, not a watch-cache copy that
	// might already be stale -- the same "re-Get immediately before acting"
	// discipline CLAUDE.md's lost-update hazard describes for an apply, here
	// applied to a delete instead. A conflict means something changed dl
	// between the Get above and here; that update already produced its own
	// watch event (the predicate matches on the label, not on a specific
	// field), so this reconcile can simply stop rather than retry blind.
	if err := s.Client.Delete(ctx, &dl, &client.DeleteOptions{
		Preconditions: &metav1.Preconditions{ResourceVersion: &dl.ResourceVersion},
	}); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
			return ctrl.Result{}, nil
		}
		log.Error("delete expired blocklist entry", "error", err)
		return ctrl.Result{}, err
	}

	log.Info("swept expired blocklist entry", "blocklistedUntil", until)
	if s.Recorder != nil {
		s.Recorder.Eventf(&dl, nil, "Normal", ReasonBlocklistExpired, "Sweep",
			"deleted expired blocklist entry (blocklistedUntil=%s)", until.Format(time.RFC3339))
	}
	return ctrl.Result{}, nil
}

// SetupWithManager registers the blocklist sweeper.
func (s *BlocklistSweeper) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("downloadclient-blocklist").
		For(&downloadv1alpha1.Download{}, builder.WithPredicates(
			k8s.HasLabel(downloadv1alpha1.LabelBlocklisted, downloadv1alpha1.LabelBlocklistedValue))).
		WithOptions(controller.Options{ReconciliationTimeout: 5 * time.Minute}).
		Complete(s)
}
