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

package iptvchannel

import (
	"context"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	clustarrac "github.com/mediactl/clustarr/api/applyconfiguration/clustarr/v1alpha1"
	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/index"
	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// reasonPending is a channel whose provider the tuner has not rendered yet.
const reasonPending = "Pending"

// reasonKeep is place's answer for a channel whose status stands as it is.
const reasonKeep = ""

// Snapshots is what the reconciler needs of app/livetv/tuner.
type Snapshots interface {
	// Channel is one channel as the tuner last rendered it: one lookup per
	// reconcile, never a copy of the provider's whole snapshot.
	Channel(key types.NamespacedName, object string) (tuner.ChannelView, bool)
}

// Reconciler reconciles IPTVChannel status.
type Reconciler struct {
	Client    client.Client
	Snapshots Snapshots
	// Events carries the tuner's "snapshot changed" notices, one per
	// provider; each wakes that provider's channels.
	Events <-chan event.GenericEvent
}

// Reconcile writes one channel's status when it changed.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var ch clustarrv1.IPTVChannel
	if err := r.Client.Get(ctx, req.NamespacedName, &ch); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	reason, guide := r.place(ctx, &ch)
	if reason == reasonKeep {
		return ctrl.Result{}, nil
	}
	status := metav1.ConditionFalse
	switch reason {
	case clustarrv1.ReasonActive:
		status = metav1.ConditionTrue
	case reasonPending:
		status = metav1.ConditionUnknown
	}
	conds := slices.Clone(ch.Status.Conditions)
	changed := meta.SetStatusCondition(&conds, metav1.Condition{
		Type: clustarrv1.ConditionReady, Status: status, Reason: reason,
		Message: message(reason), ObservedGeneration: ch.Generation,
	})
	if !changed && ch.Status.Guide == guide && ch.Status.ObservedGeneration == ch.Generation {
		return ctrl.Result{}, nil
	}
	st := clustarrac.IPTVChannelStatus().WithObservedGeneration(ch.Generation)
	if guide != "" {
		st.WithGuide(guide)
	}
	for _, c := range conds {
		if c.Type == clustarrv1.ConditionReady {
			st.WithConditions(metav1ac.Condition().WithType(c.Type).WithStatus(c.Status).WithReason(c.Reason).
				WithMessage(c.Message).WithLastTransitionTime(c.LastTransitionTime).WithObservedGeneration(c.ObservedGeneration))
		}
	}
	_, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerLiveTVChannel, clustarrac.IPTVChannel(ch.Name, ch.Namespace).WithStatus(st))
	return ctrl.Result{}, err
}

// place is the channel's Ready reason and the guide it uses.
func (r *Reconciler) place(ctx context.Context, ch *clustarrv1.IPTVChannel) (string, string) {
	key := types.NamespacedName{Namespace: ch.Namespace, Name: ch.Spec.ProviderRef}
	view, ok := r.Snapshots.Channel(key, ch.Name)
	if !ok {
		var p clustarrv1.IPTVProvider
		if err := r.Client.Get(ctx, key, &p); apierrors.IsNotFound(err) {
			return clustarrv1.ReasonProviderNotFound, ""
		} else if err != nil {
			logging.FromContext(ctx).Warn("livetv: read the channel's provider", "provider", key.String(), "err", err)
		}
		return reasonPending, ""
	}
	st, has := view.State, view.Listed
	switch {
	case !has && !view.PlaylistFetched && meta.FindStatusCondition(ch.Status.Conditions, clustarrv1.ConditionReady) != nil:
		// The tuner has not fetched since it (re)started: what the status
		// says is the best there is until it has.
		return reasonKeep, ""
	case !has:
		return reasonPending, ""
	case st.Missing:
		return clustarrv1.ReasonMissing, ""
	case st.Duplicate:
		return clustarrv1.ReasonDuplicateNumber, ""
	case st.GuideNotFound:
		return clustarrv1.ReasonGuideNotFound, st.Guide
	case ch.Spec.Active:
		return clustarrv1.ReasonActive, st.Guide
	}
	return clustarrv1.ReasonInactive, ""
}

func message(reason string) string {
	switch reason {
	case clustarrv1.ReasonActive:
		return "in the provider's lineup"
	case clustarrv1.ReasonInactive:
		return "spec.active is false"
	case clustarrv1.ReasonMissing:
		return "the key is not a candidate: gone from the playlist, or filtered out"
	case clustarrv1.ReasonDuplicateNumber:
		return "another active channel of the provider has this number"
	case clustarrv1.ReasonGuideNotFound:
		return "spec.epg.guide names no guide of the provider; a dummy guide is used"
	case clustarrv1.ReasonProviderNotFound:
		return "spec.providerRef names no IPTVProvider"
	}
	return "the tuner has not rendered the provider yet"
}

// SetupWithManager registers the controller and its watches: the channel,
// and the tuner's provider notices through the providerRef index.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := index.Register(context.Background(), mgr); err != nil {
		return err
	}
	c := mgr.GetClient()
	channelsOf := func(ctx context.Context, o client.Object) []reconcile.Request {
		chans, err := index.ChannelsOf(ctx, c, o.GetNamespace(), o.GetName())
		if err != nil {
			logging.FromContext(ctx).Warn("livetv: list a provider's channels", "provider", o.GetNamespace()+"/"+o.GetName(), "err", err)
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(chans))
		for _, ch := range chans {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ch.Namespace, Name: ch.Name}})
		}
		return reqs
	}
	b := ctrl.NewControllerManagedBy(mgr).
		Named("iptvchannel").
		For(&clustarrv1.IPTVChannel{}).
		// A channel's status reads the tuner and whether its provider
		// exists, not the provider's status, which changes with every tune.
		Watches(&clustarrv1.IPTVProvider{}, handler.EnqueueRequestsFromMapFunc(channelsOf),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}))
	if r.Events != nil {
		b = b.WatchesRawSource(source.Channel(r.Events, handler.EnqueueRequestsFromMapFunc(channelsOf)))
	}
	return b.Complete(r)
}
