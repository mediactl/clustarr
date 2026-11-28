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

package downloadclient

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	autoscale "github.com/mediactl/clustarr/app/autoscale/controller"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/remediation/dlindex"
	"github.com/mediactl/clustarr/pkg/download/usenetaccount"
	cevents "github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The usenet engine fleet (spec 2026-11-27): bandwidth budgets from the
// engines' nodes, the engine count from the backlog, and the metered
// providers' use of their quotas. A client that sets none of
// usenet.bandwidth, usenet.autoscale and replicas above one runs the one
// engine it always has, and nothing here changes what it is told.
const (
	// drainIdle is how long the draining ordinal holds nothing before it
	// is removed, and drainLook how soon a draining ordinal that still
	// holds something is looked at again: a grab past its transfer leaves
	// it when its entry is dropped, which wakes nothing here.
	drainIdle = 10 * time.Minute
	drainLook = time.Minute
	// quotaStepBytes is the least step a quota's used bytes move by in
	// status (else a hundredth of the quota): status is written when
	// something a person would act on changed, and the live figure is the
	// engines' metrics.
	quotaStepBytes = 1 << 30
	// quotaWarnPercent is the share of a quota used at which QuotaOK turns
	// False.
	quotaWarnPercent = 90

	defaultLinkSpeed      = "1G"
	defaultPerTransfer    = "250M"
	defaultRequestPercent = 25
	defaultLimitPercent   = 50
	// defaultMaxActive is usenet.maxActive's default, the client's own
	// (pkg/download/usenet's defaultMaxActive).
	defaultMaxActive = 2

	// ReasonQuotaNearlySpent means a metered provider has used
	// quotaWarnPercent of its quota or more.
	ReasonQuotaNearlySpent = "QuotaNearlySpent"
)

// link is what an engine's node allows it, in bits per second.
type link struct{ request, limit int64 }

// nodeLink reads an engine's request and limit off its node: the node's
// own annotations where it states them, else bw's percentages of its link
// (the link-speed annotation, else bw's default). node may be nil: an
// engine that has not said where it runs is on a default link.
func nodeLink(node *corev1.Node, bw *downloadv1alpha1.BandwidthSpec) link {
	quantity := func(key string) (int64, bool) {
		if node == nil {
			return 0, false
		}
		q, err := resource.ParseQuantity(node.Annotations[key])
		if err != nil || q.Value() <= 0 {
			return 0, false
		}
		return q.Value(), true
	}
	speed, ok := quantity(downloadv1alpha1.NodeLinkSpeedAnnotation)
	if !ok {
		speed = quantityBits(defaultLinkSpeed)
		if bw.DefaultLinkSpeed != nil && bw.DefaultLinkSpeed.Value() > 0 {
			speed = bw.DefaultLinkSpeed.Value()
		}
	}
	percent := func(p, fallback int32) int64 {
		if p <= 0 {
			p = fallback
		}
		return speed / 100 * int64(p)
	}
	l := link{request: percent(bw.RequestPercent, defaultRequestPercent), limit: percent(bw.LimitPercent, defaultLimitPercent)}
	if v, ok := quantity(downloadv1alpha1.NodeBandwidthRequestAnnotation); ok {
		l.request = v
	}
	if v, ok := quantity(downloadv1alpha1.NodeBandwidthLimitAnnotation); ok {
		l.limit = v
	}
	l.request = min(l.request, l.limit)
	return l
}

// quantityBits is a rate constant ("250M") in bits per second.
func quantityBits(s string) int64 {
	q := resource.MustParse(s)
	return q.Value()
}

// budgetOf is one engine's budget before its sequence number.
type budgetOf struct {
	bits, limit int64
	transfers   int32
}

// computeBudgets divides bw among the engines (spec 2026-11-27 §5.2). An
// engine with nothing to do is given its request and counts for nothing
// else. The engaged ones are given their requests -- scaled down together
// when those alone pass the total -- and share what is left of the total
// in proportion to the room between each one's request and limit, none
// past its limit. With no total every engaged engine has its limit. An
// engine runs its budget over perTransfer transfers at once, at least one
// and at most maxActive.
func computeBudgets(links map[int32]link, engaged map[int32]bool, bw *downloadv1alpha1.BandwidthSpec, maxActive int32) map[int32]budgetOf {
	var total int64
	if bw.Total != nil {
		total = bw.Total.Value()
	}
	perTransfer := quantityBits(defaultPerTransfer)
	if bw.PerTransfer != nil && bw.PerTransfer.Value() > 0 {
		perTransfer = bw.PerTransfer.Value()
	}
	var requests, room int64
	for o, l := range links {
		if engaged[o] {
			requests += l.request
			room += l.limit - l.request
		}
	}
	out := make(map[int32]budgetOf, len(links))
	for o, l := range links {
		bits := l.request
		switch {
		case !engaged[o]:
		case total <= 0:
			bits = l.limit
		case requests > total:
			// Floating point here and below: the products of two rates
			// in bits per second pass an int64.
			bits = int64(float64(l.request) / float64(requests) * float64(total))
		case total-requests >= room:
			bits = l.limit
		default:
			bits = l.request + int64(float64(l.limit-l.request)/float64(room)*float64(total-requests))
		}
		transfers := int32(min(max((bits+perTransfer/2)/perTransfer, 1), int64(max(maxActive, 1))))
		out[o] = budgetOf{bits: bits, limit: l.limit, transfers: transfers}
	}
	return out
}

// scaleInput is what the engine count is decided from.
type scaleInput struct {
	// current is the count rendered now; min and max bound it.
	current, min, max int32
	// waiting is the downloads waiting for a transfer slot.
	waiting int
	// pinned and capacity are, per ordinal, the downloads holding a slot
	// and the slots it has.
	pinned   map[int32]int
	capacity map[int32]int32
	// ready reports every rendered engine Ready on a fresh record: another
	// is not added while one is still starting.
	ready bool
	// draining is the ordinal being emptied, and idleFor how long nothing
	// at all has been pinned to it (0 while something is).
	draining *int32
	idleFor  time.Duration
}

// scale decides a usenet client's engine count (spec 2026-11-27 §8).
//
//   - Up: downloads wait for a slot and every engine is full. A draining
//     ordinal is first taken back into service; else one engine is added,
//     once every rendered one is ready.
//   - Down: nothing waits, more engines run than the minimum, and the
//     engines below the highest ordinal have slots for everything pinned
//     anywhere. The highest ordinal is then drained -- given nothing new --
//     and removed once it has held nothing for drainIdle.
func scale(in scaleInput) (desired int32, draining *int32) {
	desired = min(max(in.current, in.min), in.max)
	if desired != in.current {
		return desired, nil
	}
	full := true
	var pinnedAll, capBelow int
	for o := range desired {
		pinnedAll += in.pinned[o]
		if o < desired-1 {
			capBelow += int(in.capacity[o])
		}
		if in.draining != nil && *in.draining == o {
			continue
		}
		if in.pinned[o] < int(in.capacity[o]) {
			full = false
		}
	}
	if in.waiting > 0 {
		switch {
		case !full:
			// A slot is free: the waiters are about to be assigned.
			return desired, in.draining
		case in.draining != nil:
			return desired, nil
		case desired < in.max && in.ready:
			return desired + 1, nil
		}
		return desired, nil
	}
	if desired <= in.min || pinnedAll > capBelow {
		return desired, nil
	}
	top := desired - 1
	if in.draining != nil && *in.draining == top && in.idleFor >= drainIdle {
		return desired - 1, nil
	}
	return desired, &top
}

// fleetState is what one reconcile decided for a usenet client's engines.
type fleetState struct {
	replicas int32
	draining *int32
	waiting  int32
	// budgets is each rendered ordinal's budget, nil without
	// usenet.bandwidth; nodes each ordinal's node, from its record.
	budgets map[int32]budgetOf
	nodes   map[int32]string
	// due is how soon the fleet needs another look for a reason nothing
	// announces: a drain's grace running out. Zero is none.
	due time.Duration
}

// fixedReplicas is spec.replicas, floored at one.
func fixedReplicas(dc *downloadv1alpha1.DownloadClient) int32 { return max(dc.Spec.Replicas, 1) }

// currentReplicas is the usenet engine count the controller last rendered:
// status.engine.desiredReplicas under usenet.autoscale, within its bounds,
// else spec.replicas.
func currentReplicas(dc *downloadv1alpha1.DownloadClient) int32 {
	as := autoscaleOf(dc)
	if as == nil {
		return fixedReplicas(dc)
	}
	lo, hi := autoscaleBounds(as)
	cur := max(lo, 1)
	if e := dc.Status.Engine; e != nil && e.DesiredReplicas != nil {
		cur = *e.DesiredReplicas
	}
	return min(max(cur, lo), hi)
}

func autoscaleOf(dc *downloadv1alpha1.DownloadClient) *downloadv1alpha1.EngineAutoscale {
	if dc.Spec.Usenet == nil {
		return nil
	}
	return dc.Spec.Usenet.Autoscale
}

func autoscaleBounds(as *downloadv1alpha1.EngineAutoscale) (lo, hi int32) {
	lo = 1
	if as.MinReplicas != nil {
		lo = *as.MinReplicas
	}
	hi = max(as.MaxReplicas, 1)
	return min(lo, hi), hi
}

// maxActiveOf is usenet.maxActive with its default.
func maxActiveOf(dc *downloadv1alpha1.DownloadClient) int32 {
	if dc.Spec.Usenet != nil && dc.Spec.Usenet.MaxActive != nil {
		return *dc.Spec.Usenet.MaxActive
	}
	return defaultMaxActive
}

// fleet decides a usenet client's engine count, the ordinal it drains and
// its engines' budgets from the engines' records, their nodes and the
// downloads pinned to and waiting for them. es is the records of the
// count rendered now.
func (r *Reconciler) fleet(ctx context.Context, dc *downloadv1alpha1.DownloadClient, es engineState, now time.Time) (fleetState, error) {
	current := currentReplicas(dc)
	fs := fleetState{replicas: current, nodes: map[int32]string{}}
	if dc.Spec.Usenet == nil {
		fs.replicas = fixedReplicas(dc)
		return fs, nil
	}
	pinned := map[int32]int{}
	if lateBinding(dc) {
		for o := range current {
			holders, err := dlindex.SlotHolders(ctx, r.Client, dc.Namespace, lifecycle.EngineName(dc.Name, o))
			if err != nil {
				return fs, err
			}
			pinned[o] = len(holders)
		}
		waiters, err := dlindex.SlotWaiters(ctx, r.Client, dc.Namespace, dc.Name)
		if err != nil {
			return fs, err
		}
		fs.waiting = int32(min(len(waiters), 1<<30))
	}
	for o, rec := range es.records {
		fs.nodes[o] = rec.Node
	}

	capacity := map[int32]int32{}
	maxActive := maxActiveOf(dc)
	for o := range current {
		capacity[o] = maxActive
	}
	if bw := dc.Spec.Usenet.Bandwidth; bw != nil {
		links := map[int32]link{}
		engaged := map[int32]bool{}
		for o := range current {
			var node *corev1.Node
			if name := fs.nodes[o]; name != "" {
				var n corev1.Node
				switch err := r.Client.Get(ctx, types.NamespacedName{Name: name}, &n); {
				case err == nil:
					node = &n
				case !apierrors.IsNotFound(err):
					return fs, fmt.Errorf("downloadclient: node %s of engine %d: %w", name, o, err)
				}
			}
			links[o] = nodeLink(node, bw)
			rec := es.records[o]
			engaged[o] = pinned[o] > 0 || (rec != nil && rec.Active+rec.Queued > 0)
		}
		fs.budgets = computeBudgets(links, engaged, bw, maxActive)
		for o, b := range fs.budgets {
			capacity[o] = b.transfers
		}
	}

	as := autoscaleOf(dc)
	if as == nil {
		return fs, nil
	}
	lo, hi := autoscaleBounds(as)
	if n, ok := r.schedulableNodes(ctx, dc); ok {
		hi = max(min(hi, int32(n)), lo)
	}
	var draining *int32
	if e := dc.Status.Engine; e != nil && e.Draining != nil && *e.Draining < current {
		draining = e.Draining
	}
	// The draining ordinal is idle once nothing at all is pinned to it: a
	// grab past its transfer still verifies, unpacks and publishes from
	// that engine's working area, and is removed through its durable.
	drainingHolds := 0
	if draining != nil {
		owners, err := dlindex.PinnedTo(ctx, r.Client, dc.Namespace, lifecycle.EngineName(dc.Name, *draining))
		if err != nil {
			return fs, err
		}
		drainingHolds = len(owners)
	}
	key := client.ObjectKeyFromObject(dc)
	r.mu.Lock()
	if r.drainIdleSince == nil {
		r.drainIdleSince = map[types.NamespacedName]time.Time{}
	}
	var idleFor time.Duration
	if draining != nil && drainingHolds == 0 {
		since, ok := r.drainIdleSince[key]
		if !ok {
			since = now
			r.drainIdleSince[key] = now
		}
		idleFor = now.Sub(since)
	} else {
		delete(r.drainIdleSince, key)
	}
	r.mu.Unlock()

	fs.replicas, fs.draining = scale(scaleInput{
		current: current, min: lo, max: hi, waiting: int(fs.waiting),
		pinned: pinned, capacity: capacity,
		ready:    es.freshReady >= current,
		draining: draining, idleFor: idleFor,
	})
	switch {
	case fs.draining == nil:
	case drainingHolds > 0 || draining == nil:
		fs.due = drainLook
	default:
		fs.due = max(drainIdle-idleFor, time.Second)
	}
	if fs.replicas != current {
		r.mu.Lock()
		delete(r.drainIdleSince, key)
		r.mu.Unlock()
		logging.FromContext(ctx).Info("downloadclient: usenet engine count changes",
			"from", current, "to", fs.replicas, "waiting", fs.waiting)
	}
	return fs, nil
}

// schedulableNodes counts the nodes an engine pod of dc could run on, from
// the pod template of the StatefulSet it already has: the autoscaler's
// ceiling, since engines run one a node. false when there is no
// StatefulSet yet, or the nodes could not be listed: no ceiling then.
func (r *Reconciler) schedulableNodes(ctx context.Context, dc *downloadv1alpha1.DownloadClient) (int, bool) {
	var live appsv1.StatefulSet
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: dc.Namespace, Name: engineWorkloadName(dc.Name)}, &live); err != nil {
		return 0, false
	}
	var nodes corev1.NodeList
	if err := r.Client.List(ctx, &nodes); err != nil {
		logging.FromContext(ctx).Warn("downloadclient: list nodes for the engine ceiling", "err", err)
		return 0, false
	}
	n, err := autoscale.MatchingNodes(nodes.Items, &live.Spec.Template.Spec)
	if err != nil {
		return 0, false
	}
	return n, true
}

// writeBudgets writes each rendered engine's budget to
// clustarr-engine-budgets when it changed, and deletes the budgets of a
// client that has none any more. It returns each ordinal's budget for the
// status.
func (r *Reconciler) writeBudgets(ctx context.Context, dc *downloadv1alpha1.DownloadClient, fs fleetState, now time.Time) {
	if r.Bus == nil || dc.Spec.Usenet == nil {
		return
	}
	kv := r.Bus.KV(cevents.BucketEngineBudgets)
	log := logging.FromContext(ctx)
	r.mu.Lock()
	if r.budgetsWritten == nil {
		r.budgetsWritten = map[string]budgetOf{}
	}
	r.mu.Unlock()
	// One past the rendered count too: an ordinal just scaled away.
	for o := range max(fs.replicas, currentReplicas(dc)) + 1 {
		key := cevents.EngineBudgetKey(dc.Name, o)
		book := dc.Namespace + "/" + key
		b, ok := fs.budgets[o]
		r.mu.Lock()
		last, written := r.budgetsWritten[book]
		r.mu.Unlock()
		if !ok || o >= fs.replicas {
			// No budget: the engine runs without one. The delete is sent
			// once a leader, and again after any budget it wrote.
			if written && last == (budgetOf{}) {
				continue
			}
			if err := kv.Delete(ctx, key); err != nil {
				log.Warn("downloadclient: delete an engine budget", "key", key, "err", err)
				continue
			}
			r.mu.Lock()
			r.budgetsWritten[book] = budgetOf{}
			r.mu.Unlock()
			continue
		}
		if written && last == b {
			continue
		}
		val, err := json.Marshal(schema.EngineBudget{
			BitsPerSecond: b.bits, LimitBitsPerSecond: b.limit, Transfers: b.transfers,
			// The weight is the budget in megabits: the engine's claim on
			// every connection pool beside the others'.
			Weight: max(b.bits/1_000_000, 1), Seq: now.UnixMilli(), At: now,
		})
		if err != nil {
			continue
		}
		if _, err := kv.Put(ctx, key, val); err != nil {
			log.Warn("downloadclient: write an engine budget", "key", key, "err", err)
			continue
		}
		r.mu.Lock()
		r.budgetsWritten[book] = b
		r.mu.Unlock()
	}
}

// budgetStatus renders the budgets for status, in ordinal order.
func budgetStatus(dc *downloadv1alpha1.DownloadClient, fs fleetState, es engineState) []*downloadac.EngineBudgetStatusApplyConfiguration {
	ordinals := make([]int32, 0, len(fs.budgets))
	for o := range fs.budgets {
		if o < fs.replicas {
			ordinals = append(ordinals, o)
		}
	}
	slices.Sort(ordinals)
	out := make([]*downloadac.EngineBudgetStatusApplyConfiguration, 0, len(ordinals))
	for _, o := range ordinals {
		b := fs.budgets[o]
		st := downloadac.EngineBudgetStatus().
			WithEngine(lifecycle.EngineName(dc.Name, o)).
			WithBudgetBps(b.bits).
			WithTransfers(b.transfers)
		if n := fs.nodes[o]; n != "" {
			st = st.WithNode(n)
		}
		if rec := es.records[o]; rec != nil && rec.DownRateBytesPerSec > 0 {
			st = st.WithRateBps(rec.DownRateBytesPerSec * 8)
		}
		out = append(out, st)
	}
	return out
}

// quotas reads each metered provider's use of its quota from
// clustarr-usenet-usage (the engines' count, every pod's together), and
// reports whether every one is under quotaWarnPercent (judged on the exact
// count; the reported one moves in steps, [quotaStep]). A provider whose
// Secret or count cannot be read is left out: its last status goes with
// it, which says nothing is known.
func (r *Reconciler) quotas(ctx context.Context, dc *downloadv1alpha1.DownloadClient) ([]*downloadac.ProviderQuotaStatusApplyConfiguration, string) {
	us := dc.Spec.Usenet
	if us == nil || r.Bus == nil {
		return nil, ""
	}
	reader := r.SecretReader
	if reader == nil {
		reader = r.Client
	}
	kv := r.Bus.KV(cevents.BucketUsenetUsage)
	var out []*downloadac.ProviderQuotaStatusApplyConfiguration
	var warn string
	for _, p := range us.Providers {
		if p.QuotaBytes == nil || *p.QuotaBytes <= 0 {
			continue
		}
		var s corev1.Secret
		if err := reader.Get(ctx, types.NamespacedName{Namespace: dc.Namespace, Name: p.SecretRef.Name}, &s); err != nil {
			continue
		}
		u, err := usenetaccount.ReadUsage(ctx, kv, usenetaccount.KeyOf(us, p, string(s.Data["username"])))
		if err != nil {
			logging.FromContext(ctx).Warn("downloadclient: read a provider's quota count", "provider", p.Name, "err", err)
			continue
		}
		st := downloadac.ProviderQuotaStatus().WithName(p.Name).WithUsedBytes(quotaStep(u.Bytes, *p.QuotaBytes)).WithQuotaBytes(*p.QuotaBytes)
		if !u.PeriodStart.IsZero() {
			st = st.WithPeriodStart(metav1.NewTime(u.PeriodStart))
		}
		out = append(out, st)
		if u.Bytes >= *p.QuotaBytes/100*quotaWarnPercent && warn == "" {
			warn = fmt.Sprintf("provider %s has used %d of its %d quota bytes", p.Name, u.Bytes, *p.QuotaBytes)
		}
	}
	return out, warn
}

// quotaStep is used as status reports it: rounded down to a hundredth of
// the quota, at least quotaStepBytes, so a download in progress does not
// rewrite status at every reconcile.
func quotaStep(used, quota int64) int64 {
	step := max(quota/100, int64(quotaStepBytes))
	return used / step * step
}

// setQuotaCondition sets QuotaOK for a client with a metered provider, and
// removes it from one with none.
func setQuotaCondition(dc *downloadv1alpha1.DownloadClient, conditions *[]metav1.Condition, metered bool, warn string) {
	switch {
	case !metered:
		*conditions = slices.DeleteFunc(*conditions, func(c metav1.Condition) bool {
			return c.Type == downloadv1alpha1.DownloadClientConditionQuotaOK
		})
	case warn != "":
		k8s.MarkFalse(dc, conditions, downloadv1alpha1.DownloadClientConditionQuotaOK, ReasonQuotaNearlySpent, "%s", warn)
	default:
		k8s.MarkTrue(dc, conditions, downloadv1alpha1.DownloadClientConditionQuotaOK, k8s.ReasonReconciled,
			"every metered provider is under %d%% of its quota", quotaWarnPercent)
	}
}

// validatePools refuses a connection pool whose providers name different
// Secrets: a pool is one account's hosts under that account's cap, and two
// logins would be two accounts sharing nothing.
func validatePools(dc *downloadv1alpha1.DownloadClient) error {
	us := dc.Spec.Usenet
	if us == nil {
		return nil
	}
	secret := map[string]string{}
	for _, p := range us.Providers {
		if p.Pool == "" {
			continue
		}
		if first, ok := secret[p.Pool]; ok && first != p.SecretRef.Name {
			return fmt.Errorf("downloadclient: connection pool %q is one account, but its providers name the Secrets %q and %q",
				p.Pool, first, p.SecretRef.Name)
		}
		secret[p.Pool] = p.SecretRef.Name
	}
	return nil
}

// retireDeployment deletes the Deployment a usenet engine ran as before it
// was a StatefulSet, and reports whether it is gone. The delete is
// foreground, so the Deployment stays until its pod has stopped: the
// StatefulSet is applied only after that, and two engines never run under
// one identity on one working area.
func (r *Reconciler) retireDeployment(ctx context.Context, namespace, name string) (bool, error) {
	var live appsv1.Deployment
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &live); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("downloadclient: get Deployment %s: %w", name, err)
	}
	if live.DeletionTimestamp == nil {
		logging.FromContext(ctx).Info("downloadclient: the usenet engine becomes a StatefulSet; stopping its Deployment first", "deployment", name)
		if err := r.Client.Delete(ctx, &live, client.PropagationPolicy(metav1.DeletePropagationForeground),
			client.Preconditions{UID: &live.UID}); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("downloadclient: delete Deployment %s: %w", name, err)
		}
	}
	return false, nil
}
