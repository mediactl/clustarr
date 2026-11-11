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

package lineup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
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
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/iptv/blocks"
	"github.com/mediactl/clustarr/pkg/iptv/lineup"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Event reasons on the provider.
const (
	ReasonLineupApplied         = "LineupApplied"
	ReasonLineupCategoryTooMany = "LineupCategoryTooMany"
	ReasonLineupCollapseRefused = "LineupCollapseRefused"
	ReasonLineupCollapseAllowed = "LineupCollapseAllowed"
)

// entriesRetry is how soon a provider whose playlist the tuner has not
// fetched yet is asked about again.
const entriesRetry = 30 * time.Second

// storeTimeout bounds one read or write of the lineup-plan object.
const storeTimeout = 10 * time.Second

// Reconciler plans an IPTVProvider's spec.lineup and applies it to the
// provider's IPTVChannels.
type Reconciler struct {
	client.Client
	// Entries are the playlist entries the provider's filters admit, as the
	// tuner holds them (tuner.Tuner.Entries); ok is false before its first
	// fetch.
	Entries func(types.NamespacedName) ([]m3u.Entry, bool)
	// Store is the clustarr-livetv object store the plan is written to.
	Store events.ObjectStore
	// Events carries the tuner's "snapshot changed" notices: a playlist
	// fetch is one.
	Events <-chan event.GenericEvent
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// APIReader reads a channel the cache does not show before the lineup
	// creates it, so one that exists keeps its label and is not counted as
	// created; the manager's API reader. Nil reads through Client.
	APIReader client.Reader
	// Wake wakes the provider controller for a provider, which reads the
	// plan object: nothing else tells it the object moved when no channel
	// does (an error, or a dry run that changes no status). Called after
	// every plan write; nil wakes nothing.
	Wake func(types.NamespacedName)
	// Recorder records the provider's LineupApplied, LineupCategoryTooMany,
	// LineupCollapseRefused and LineupCollapseAllowed Events;
	// SetupWithManager sets the manager's when nil, and a nil one records
	// nothing.
	Recorder k8sevents.EventRecorder

	mu sync.Mutex
	// stored is the digest of each provider's last plan object written by
	// this process. Every pass writes the object (a lost one is rewritten at
	// the next); the digest only keeps the LineupCategoryTooMany Event to a
	// plan that changed.
	stored map[types.NamespacedName][sha256.Size]byte
}

// counts are what one reconcile changed.
type counts struct {
	created, renumbered, activated, deactivated, deleted, statuses int
}

func (c counts) any() bool { return c != counts{} }

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile plans one provider's lineup and applies it.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx = logging.Subsystem(ctx, "livetv.lineup")
	key := req.NamespacedName
	var p clustarrv1.IPTVProvider
	if err := r.Get(ctx, key, &p); err != nil {
		if apierrors.IsNotFound(err) {
			r.forget(key)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// A disabled provider is not served, so the tuner has no playlist for
	// it: re-enabling it is a generation change, which wakes this again.
	if !p.DeletionTimestamp.IsZero() || p.Spec.Lineup == nil || !p.Spec.EnabledOrDefault() {
		return ctrl.Result{}, nil
	}
	now := r.now()
	enabled := p.Spec.Lineup.Enabled
	// The collapse guard's override, and whether it has let a plan through
	// while it has been set (the stored plan remembers; it forgets once the
	// annotation is gone).
	allowCollapse, collapseUsed := p.Annotations[clustarrv1.AnnotationLineupAllowCollapse] == "true", false
	if allowCollapse {
		prev, ok, err := r.readPlan(ctx, key)
		if err != nil {
			return ctrl.Result{}, err
		}
		collapseUsed = ok && prev.CollapseAllowed
	}

	planner, err := lineup.Compile(lineup.FromSpec(p.Spec.Lineup, int(p.Spec.MaxChannelsOrDefault())))
	if err != nil {
		// The provider controller reads the error as InvalidLineup; the last
		// good plan, and every channel, stand.
		if _, err := r.storeError(ctx, key, now, !enabled, collapseUsed, err); err != nil {
			return ctrl.Result{}, err
		}
		r.wake(key)
		return ctrl.Result{}, nil
	}
	entries, ok := r.Entries(key)
	if !ok {
		return ctrl.Result{RequeueAfter: entriesRetry}, nil
	}
	channels, err := index.ChannelsOf(ctx, r.Client, key.Namespace, key.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	byKey := index.ChannelsByKey(p.Name, channels)
	plan, overBytes := refuseOverBytes(planner.Plan(toEntries(entries), overrides(byKey, entries, planner.Trim)))
	cs := changes(p.Name, plan, channels, byKey, enabled, now)

	// The collapse guard: a plan that would take more than half of the
	// active channels off every DVR (and at least ten) is refused as an
	// invalid one is -- its error stored beside the last good plan, nothing
	// written to a channel -- unless the provider's lineup-allow-collapse
	// annotation lets it through, once.
	if dropped, active := deactivations(cs, channels); enabled && collapses(dropped, active) {
		if !allowCollapse || collapseUsed {
			refusal := fmt.Errorf("the plan would deactivate %d of %d active channels; refusing", dropped, active)
			changed, err := r.storeError(ctx, key, now, false, collapseUsed, refusal)
			if err != nil {
				return ctrl.Result{}, err
			}
			r.wake(key)
			if changed {
				how := fmt.Sprintf("set the %s annotation to \"true\" to let it through once", clustarrv1.AnnotationLineupAllowCollapse)
				if collapseUsed {
					how = fmt.Sprintf("the %s annotation has let one through already: remove it and set it again to let another through", clustarrv1.AnnotationLineupAllowCollapse)
				}
				r.event(&p, corev1.EventTypeWarning, ReasonLineupCollapseRefused, "%s; %s", refusal, how)
				logging.FromContext(ctx).Warn("livetv: lineup plan refused: it would deactivate most of the lineup",
					"provider", key.String(), "deactivate", dropped, "active", active)
			}
			return ctrl.Result{}, nil
		}
		collapseUsed = true
		r.event(&p, corev1.EventTypeNormal, ReasonLineupCollapseAllowed,
			"the %s annotation let a plan through that deactivates %d of %d active channels", clustarrv1.AnnotationLineupAllowCollapse, dropped, active)
	}
	planChanged, err := r.storePlan(ctx, key, livetvstore.LineupPlan{PlannedAt: now, DryRun: !enabled, CollapseAllowed: collapseUsed, Plan: plan})
	if err != nil {
		return ctrl.Result{}, err
	}

	var n counts
	var errs []error
	for i := range cs {
		if err := r.apply(ctx, &p, &cs[i], &n); err != nil {
			errs = append(errs, err)
		}
	}
	r.wake(key)
	if !enabled {
		return ctrl.Result{}, errors.Join(errs...)
	}
	if planChanged {
		for _, c := range plan.Categories {
			if b, ok := overBytes[c.Name]; ok {
				r.event(&p, corev1.EventTypeWarning, ReasonLineupCategoryTooMany,
					"category %q's channel map is %d bytes, over the %d a DVR takes: its channels are left as they were and its published block is kept",
					c.Name, b, blocks.DefaultMaxBytes)
			} else if c.TooMany {
				r.event(&p, corev1.EventTypeWarning, ReasonLineupCategoryTooMany,
					"category %q has %d channels, over the %d a DVR takes: its channels are left as they were and its published block is kept",
					c.Name, c.Channels, p.Spec.MaxChannelsOrDefault())
			}
		}
	}
	if n.any() {
		r.event(&p, corev1.EventTypeNormal, ReasonLineupApplied,
			"%d created, %d renumbered, %d activated, %d deactivated, %d deleted, %d statuses written",
			n.created, n.renumbered, n.activated, n.deactivated, n.deleted, n.statuses)
		logging.FromContext(ctx).Info("livetv: lineup applied", "provider", key.String(),
			"created", n.created, "renumbered", n.renumbered, "activated", n.activated,
			"deactivated", n.deactivated, "deleted", n.deleted, "statuses", n.statuses)
	}
	return ctrl.Result{RequeueAfter: nextDeletion(cs, now)}, errors.Join(errs...)
}

// apply carries one change out: its spec (providerRef, key, number,
// active, and the origin label when it is the lineup's), then its
// status.lineup, each only when it differs from the object read; or its
// deletion.
func (r *Reconciler) apply(ctx context.Context, p *clustarrv1.IPTVProvider, c *change, n *counts) error {
	if c.remove {
		uid := c.ch.UID
		switch err := r.Delete(ctx, c.ch, client.Preconditions{UID: &uid}); {
		case apierrors.IsNotFound(err):
		case err != nil:
			return fmt.Errorf("livetv lineup: delete %s/%s: %w", p.Namespace, c.name, err)
		default:
			n.deleted++
		}
		return nil
	}
	if c.ch == nil && c.spec {
		// The cache shows no channel of the key: read the name through the
		// apiserver before creating it, so an existing one (made a moment
		// ago, or by the ui) is updated as existing -- its label its own.
		var existing clustarrv1.IPTVChannel
		switch err := r.reader().Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: c.name}, &existing); {
		case err == nil:
			c.ch, c.label = &existing, madeByLineup(&existing)
		case !apierrors.IsNotFound(err):
			return fmt.Errorf("livetv lineup: read %s/%s: %w", p.Namespace, c.name, err)
		}
	}
	if c.spec && !specMatches(p.Name, c) {
		ac := clustarrac.IPTVChannel(c.name, p.Namespace).WithSpec(clustarrac.IPTVChannelSpec().
			WithProviderRef(p.Name).WithKey(c.key).WithNumber(c.number).WithActive(c.active))
		if c.label {
			ac.WithLabels(map[string]string{clustarrv1.LabelOrigin: clustarrv1.OriginLineup})
		}
		if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerLiveTVLineup, ac); err != nil {
			return fmt.Errorf("livetv lineup: %w", err)
		}
		if c.ch == nil {
			n.created++
		} else {
			if c.ch.Spec.Number != c.number {
				n.renumbered++
			}
			if c.active && !c.ch.Spec.Active {
				n.activated++
			}
			if !c.active && c.ch.Spec.Active {
				n.deactivated++
			}
		}
	}
	st := clampStatus(c.status)
	if c.ch != nil && equality.Semantic.DeepEqual(c.ch.Status.Lineup, &st) {
		return nil
	}
	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerLiveTVLineup,
		clustarrac.IPTVChannel(c.name, p.Namespace).WithStatus(clustarrac.IPTVChannelStatus().WithLineup(lineupStatus(st)))); err != nil {
		return fmt.Errorf("livetv lineup: %w", err)
	}
	n.statuses++
	return nil
}

// specMatches is an existing channel already as the change would apply it.
func specMatches(provider string, c *change) bool {
	ch := c.ch
	return ch != nil && ch.Spec.ProviderRef == provider && ch.Spec.Key == c.key &&
		ch.Spec.Number == c.number && ch.Spec.Active == c.active &&
		(!c.label || madeByLineup(ch))
}

// lineupStatus is st, clamped, as its apply configuration, its empty fields
// unsent.
func lineupStatus(st clustarrv1.ChannelLineupStatus) *clustarrac.ChannelLineupStatusApplyConfiguration {
	ac := clustarrac.ChannelLineupStatus()
	if st.Name != "" {
		ac.WithName(st.Name)
	}
	if st.Category != "" {
		ac.WithCategory(st.Category)
	}
	if st.Rule != "" {
		ac.WithRule(st.Rule)
	}
	if st.Number != "" {
		ac.WithNumber(st.Number)
	}
	if st.DuplicateOf != "" {
		ac.WithDuplicateOf(st.DuplicateOf)
	}
	if st.MissingSince != nil {
		ac.WithMissingSince(*st.MissingSince)
	}
	return ac
}

func (r *Reconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *Reconciler) wake(key types.NamespacedName) {
	if r.Wake != nil {
		r.Wake(key)
	}
}

func (r *Reconciler) event(p *clustarrv1.IPTVProvider, eventType, reason, note string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(p, nil, eventType, reason, "Lineup", note, args...)
	}
}

// storePlan writes lp as the provider's lineup-plan object, every pass, so
// an object lost while the manager runs is back at the next; it reports
// whether the plan differs (PlannedAt aside) from the last this process
// wrote.
func (r *Reconciler) storePlan(ctx context.Context, key types.NamespacedName, lp livetvstore.LineupPlan) (bool, error) {
	digest, err := planDigest(lp)
	if err != nil {
		return false, err
	}
	var b bytes.Buffer
	if err := livetvstore.Encode(&b, lp); err != nil {
		return false, fmt.Errorf("livetv lineup: encode the plan of %s: %w", key, err)
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	name := livetvstore.LineupPlanObject(key)
	if _, err := r.Store.Put(ctx, name, &b, objectMeta(name)); err != nil {
		return false, fmt.Errorf("livetv lineup: store %s: %w", name, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stored == nil {
		r.stored = map[types.NamespacedName][sha256.Size]byte{}
	}
	changed := r.stored[key] != digest
	r.stored[key] = digest
	return changed, nil
}

// storeError stores a plan that failed to compile, or that the collapse
// guard refused: the error beside the last good plan stored, which stands.
// collapseAllowed carries the guard's memory of its override. It reports
// whether the object says something new.
func (r *Reconciler) storeError(ctx context.Context, key types.NamespacedName, now time.Time, dryRun, collapseAllowed bool, planErr error) (bool, error) {
	lp := livetvstore.LineupPlan{PlannedAt: now, DryRun: dryRun, Error: planErr.Error(), CollapseAllowed: collapseAllowed}
	if prev, ok, err := r.readPlan(ctx, key); err != nil {
		return false, err
	} else if ok {
		lp.Plan = prev.Plan
	}
	return r.storePlan(ctx, key, lp)
}

// readPlan reads the provider's stored plan; ok is false when none is
// stored or it does not decode.
func (r *Reconciler) readPlan(ctx context.Context, key types.NamespacedName) (*livetvstore.LineupPlan, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	name := livetvstore.LineupPlanObject(key)
	_, rc, err := r.Store.Get(ctx, name)
	if errors.Is(err, events.ErrObjectNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("livetv lineup: read %s: %w", name, err)
	}
	defer func() { _ = rc.Close() }()
	var lp livetvstore.LineupPlan
	if err := livetvstore.Decode(rc, &lp); err != nil {
		logging.FromContext(ctx).Warn("livetv: the stored lineup plan does not decode", "object", name, "err", err)
		return nil, false, nil
	}
	return &lp, true, nil
}

// planDigest digests what a plan object says, PlannedAt aside.
func planDigest(lp livetvstore.LineupPlan) ([sha256.Size]byte, error) {
	b, err := json.Marshal(struct {
		DryRun bool
		Error  string
		Plan   lineup.Plan
	}{lp.DryRun, lp.Error, lp.Plan})
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("livetv lineup: digest the plan: %w", err)
	}
	return sha256.Sum256(b), nil
}

func (r *Reconciler) forget(key types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.stored, key)
}

// SetupWithManager registers the controller: the provider (a spec change,
// or its lineup-allow-collapse annotation), its channels (a person's edit,
// a create or a delete) and the tuner's notices (a playlist fetch).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := index.Register(context.Background(), mgr); err != nil {
		return err
	}
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorder("livetv-lineup")
	}
	toProvider := func(_ context.Context, o client.Object) []reconcile.Request {
		ch, ok := o.(*clustarrv1.IPTVChannel)
		if !ok || ch.Spec.ProviderRef == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: ch.Namespace, Name: ch.Spec.ProviderRef}}}
	}
	b := ctrl.NewControllerManagedBy(mgr).
		Named("iptvlineup").
		For(&clustarrv1.IPTVProvider{}, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{},
			k8s.AnnotationsChanged(clustarrv1.AnnotationLineupAllowCollapse)))).
		// A channel's status moves with every render; only its spec (a
		// person's name or category) and labels feed the plan.
		Watches(&clustarrv1.IPTVChannel{}, handler.EnqueueRequestsFromMapFunc(toProvider),
			builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})))
	if r.Events != nil {
		b = b.WatchesRawSource(source.Channel(r.Events, &handler.EnqueueRequestForObject{}))
	}
	return b.Complete(r)
}
