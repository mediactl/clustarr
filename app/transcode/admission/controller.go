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

package admission

import (
	"context"
	"fmt"
	"slices"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/graftplan"
	"github.com/mediactl/clustarr/app/transcode/grafttask"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// ControllerName is the admission controller's name (loop spec §5.3, D29).
const ControllerName = "transcode-admission"

// Request is the admission controller's one key: every pass considers every
// file, so there is nothing to key by.
var Request = reconcile.Request{NamespacedName: types.NamespacedName{Name: "(admission)"}}

// The admission controller reads nodes, the pool and graft Jobs, MediaFiles
// (the rebuild's quorum Lists and the backlog scan), the items, the
// profiles and cluster-plex's video-transcode Lease (videolease.go). It
// writes nothing. The blank line keeps the markers package-level.
//
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;episodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodeprofiles,verbs=get;list;watch

// Reconciler is the transcode-admission controller (loop spec §5.7): one
// pass at a time over the ledger, from the cache and clustarr-progress. It
// decides every grant and directive and writes only memory; the loop's
// transcode planner writes status.transcode, and the slim TranscodeProfile
// reconciler the pools.
type Reconciler struct {
	Client    client.Client
	APIReader client.Reader
	Ledger    *Ledger
	Bus       events.Bus         // clustarr-progress: health, limits, tier (§5.7 step 1)
	Admin     events.StreamAdmin // the task sweep (§5.6)
	// Withdrawal withdraws a deleted file's task (the sweep's WithdrawAll).
	Withdrawal task.Withdrawal
	Namespaces []string
	Pool       pool.Config
	// RebuildGate holds the first pass's rebuild until it closes: tests only.
	RebuildGate <-chan struct{}
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// VideoLease is cluster-plex's video-transcode Lease
	// (--video-transcode-lease): while it counts a conversion, no new file
	// is admitted to intel. Zero reads none, and nothing yields.
	VideoLease types.NamespacedName

	nextResync time.Time
	// videoRecheck is when a live count goes stale unless renewed: the
	// pass after it may admit to intel again.
	videoRecheck time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile is one admission pass: the rebuild first (and every
// ledgerResync the resync), then Pass over what the cache and
// clustarr-progress say.
func (r *Reconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	ctx, span := tracing.Start(ctx, "admission.Reconcile")
	defer span.End()
	if !r.Ledger.Rebuilt() {
		if r.RebuildGate != nil {
			select {
			case <-r.RebuildGate:
			case <-ctx.Done():
				return reconcile.Result{}, ctx.Err()
			}
		}
		if err := r.Ledger.Rebuild(ctx, r.rebuildInputs()); err != nil {
			return reconcile.Result{}, err
		}
		r.nextResync = r.now().Add(ledgerResync)
		r.logSweep(ctx)
	} else if r.now().After(r.nextResync) {
		if err := r.Ledger.Resync(ctx, r.rebuildInputs()); err != nil {
			return reconcile.Result{}, err
		}
		r.nextResync = r.now().Add(ledgerResync)
		r.logSweep(ctx)
	}
	in, err := r.passInputs(ctx)
	if err != nil {
		return reconcile.Result{}, err
	}
	res := r.Ledger.Pass(in)
	after := min(res.RequeueAfter, time.Until(r.nextResync))
	if in.VideoStreams > 0 {
		after = min(after, r.videoRecheck.Sub(r.now()))
	}
	return reconcile.Result{RequeueAfter: max(after, time.Second)}, nil
}

// logSweep runs the task sweep; a failure is logged, and the next resync
// sweeps again.
func (r *Reconciler) logSweep(ctx context.Context) {
	if err := r.sweep(ctx); err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "admission: the task sweep failed; the next resync sweeps again", "error", err)
	}
}

// rebuildInputs is what the rebuild reads.
func (r *Reconciler) rebuildInputs() RebuildInputs {
	return RebuildInputs{APIReader: r.APIReader, Cache: r.Client, Namespaces: r.Namespaces, Eligible: r.eligible}
}

// eligible is the backlog scan's test (§5.6 step 3): transcodeplan.Eligible
// over the cached profiles, the item and the file's graft block, as the
// loop's transcode planner reads them.
func (r *Reconciler) eligible(ctx context.Context, mf *catalogv1alpha1.MediaFile) *transcodeplan.Eligibility {
	var profiles transcodev1alpha1.TranscodeProfileList
	if err := r.Client.List(ctx, &profiles, client.UnsafeDisableDeepCopy); err != nil {
		return nil
	}
	exists, downloading, err := itemExists(ctx, r.Client, mf)
	if err != nil {
		return nil
	}
	f := transcodeplan.File{
		MF: mf, Labels: mf.Labels, Status: &mf.Status, ProbeCurrent: mf.ProbeCurrent(), ItemExists: exists,
		StandaloneGraft: graftplan.Standalone(mf.Status.Graft), UpgradeDownloading: downloading,
	}
	if p, ok := transcodeplan.ParsePriority(mf.Annotations[catalogv1alpha1.AnnotationTranscodePriority]); ok {
		f.Priority = &p
	}
	e := transcodeplan.Eligible(f, profiles.Items)
	return &e
}

// itemExists Gets the file's Movie or Episode from the cache: whether it
// exists, and whether it has an upgrade downloading
// (transcodeplan.UpgradeDownloading).
func itemExists(ctx context.Context, c client.Reader, mf *catalogv1alpha1.MediaFile) (exists, downloading bool, err error) {
	key := types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}
	var obj client.Object
	switch mf.Spec.MediaRef.Kind {
	case commonv1.MediaKindMovie:
		obj = &catalogv1alpha1.Movie{}
	case commonv1.MediaKindEpisode:
		obj = &catalogv1alpha1.Episode{}
	default:
		return false, false, nil
	}
	switch err := c.Get(ctx, key, obj, client.UnsafeDisableDeepCopy); {
	case apierrors.IsNotFound(err):
		return false, false, nil
	case err != nil:
		return false, false, err
	}
	return true, transcodeplan.UpgradeDownloading(obj), nil
}

// passInputs reads §5.7 step 1's inputs: profiles, nodes and pool Jobs from
// the cache; devices and their health from clustarr-progress (an unreadable
// bucket counts as no report).
func (r *Reconciler) passInputs(ctx context.Context) (PassInputs, error) {
	var in PassInputs
	var profiles transcodev1alpha1.TranscodeProfileList
	if err := r.Client.List(ctx, &profiles); err != nil {
		return in, fmt.Errorf("admission: list TranscodeProfiles: %w", err)
	}
	in.Profiles = make(map[string]ProfileView, len(profiles.Items))
	tps := make([]*transcodev1alpha1.TranscodeProfile, 0, len(profiles.Items))
	for i := range profiles.Items {
		tp := &profiles.Items[i]
		if k8s.IsDeleting(tp) {
			continue
		}
		tps = append(tps, tp)
		in.Profiles[tp.Name] = ProfileView{
			Name: tp.Name, UID: tp.UID, Priority: tp.Spec.Priority, MaxConcurrent: tp.Spec.MaxConcurrent, Hardware: tp.Spec.Hardware,
		}
	}
	gpu, err := r.gpuNodes(ctx)
	if err != nil {
		// No GPU node is the safe reading: auto work goes to cpu, which
		// every cluster has, rather than wait on a class nothing confirmed.
		logging.FromContext(ctx).WarnContext(ctx, "admission: cannot list Nodes; auto files go to cpu this pass", "error", err)
	}
	in.GPUNodes = gpu
	in.Devices, in.Unhealthy = r.devices(ctx)
	stored, err := r.poolJobs(ctx)
	if err != nil {
		return in, err
	}
	in.Held = r.Ledger.Holding(stored, tps, r.Pool)
	if in.GraftJobs, err = r.graftJobs(ctx); err != nil {
		return in, err
	}
	in.VideoStreams = r.videoStreams(ctx)
	return in, nil
}

// videoStreams reads cluster-plex's Lease from the cache. One it cannot
// read counts as none: Plex's streams then share the GPU, as before the
// yield, rather than hold clustarr's encodes on a Lease that may never come.
func (r *Reconciler) videoStreams(ctx context.Context) int {
	if r.VideoLease.Name == "" {
		return 0
	}
	var l coordinationv1.Lease
	if err := r.Client.Get(ctx, r.VideoLease, &l); err != nil {
		if !apierrors.IsNotFound(err) {
			logging.FromContext(ctx).WarnContext(ctx, "admission: cannot read the video-transcode Lease; nothing yields to Plex this pass",
				"lease", r.VideoLease.String(), "error", err)
		}
		return 0
	}
	n := videoStreams(&l, r.now())
	if n > 0 {
		r.videoRecheck = l.Spec.RenewTime.Add(videoLeaseStale + time.Second)
	}
	return n
}

// graftJobs is every unfinished graft Job's name by namespace, from the
// cache (pool.PoolJobCache holds them by their managed-by label): §5.13's
// slots in use. Every pass reads them, the rebuild's first included (§5.6
// step 2).
func (r *Reconciler) graftJobs(ctx context.Context) (map[string][]string, error) {
	var jobs batchv1.JobList
	opts := []client.ListOption{client.MatchingLabels{pool.LabelManagedBy: pool.ManagedByValue}, client.HasLabels{grafttask.LabelGraftOf}}
	if err := r.Client.List(ctx, &jobs, opts...); err != nil {
		return nil, fmt.Errorf("admission: list graft Jobs: %w", err)
	}
	out := map[string][]string{}
	for i := range jobs.Items {
		j := &jobs.Items[i]
		if done, _, _ := pool.JobFinished(j); !done && j.DeletionTimestamp == nil {
			out[j.Namespace] = append(out[j.Namespace], j.Name)
		}
	}
	return out, nil
}

// isGraftJob passes the Jobs a standalone graft runs as.
func isGraftJob(o client.Object) bool {
	_, ok := o.GetLabels()[grafttask.LabelGraftOf]
	return ok && o.GetLabels()[pool.LabelManagedBy] == pool.ManagedByValue
}

// graftJobSignal is what of a graft Job frees or takes a slot: whether it
// has finished.
func graftJobSignal(o client.Object) string {
	j, ok := o.(*batchv1.Job)
	if !ok {
		return ""
	}
	done, _, _ := pool.JobFinished(j)
	return fmt.Sprintf("finished=%t", done)
}

// poolJobs is every pool Job by name, from the cache (pool.PoolJobCache).
func (r *Reconciler) poolJobs(ctx context.Context) (map[string]*batchv1.Job, error) {
	var jobs batchv1.JobList
	opts := []client.ListOption{client.MatchingLabels{pool.LabelManagedBy: pool.ManagedByValue}, client.HasLabels{pool.LabelProfile}}
	if r.Pool.Namespace != "" {
		opts = append(opts, client.InNamespace(r.Pool.Namespace))
	}
	if err := r.Client.List(ctx, &jobs, opts...); err != nil {
		return nil, fmt.Errorf("admission: list pool Jobs: %w", err)
	}
	out := make(map[string]*batchv1.Job, len(jobs.Items))
	for i := range jobs.Items {
		out[jobs.Items[i].Name] = &jobs.Items[i]
	}
	return out, nil
}

// classTiers are the tiers a GPU class's pool encodes with.
var classTiers = map[transcodev1alpha1.Hardware][]transcode.Tier{
	transcodev1alpha1.HardwareNVIDIA: {transcode.TierNVENC},
	transcodev1alpha1.HardwareIntel:  {transcode.TierQSV, transcode.TierVAAPI},
}

// devices reads, for each GPU class, the device its pods published -- the
// limits and the tier they measured -- and whether every fresh report says
// the device cannot be used (spec §4; task.ReadEncoderHealth).
func (r *Reconciler) devices(ctx context.Context) (map[transcodev1alpha1.Hardware]transcodeplan.Device, map[transcodev1alpha1.Hardware]string) {
	devs := map[transcodev1alpha1.Hardware]transcodeplan.Device{}
	if r.Bus == nil {
		return devs, nil
	}
	kv := r.Bus.KV(events.BucketProgress)
	now := r.now()
	log := logging.FromContext(ctx)
	var unhealthy map[transcodev1alpha1.Hardware]string
	for _, class := range GPUClasses {
		if health, err := task.ReadEncoderHealth(ctx, kv, string(class), now); err != nil {
			log.WarnContext(ctx, "admission: cannot read the devices' health", "class", class, "error", err)
		} else if msg, ok := UnhealthyMessage(class, health); ok {
			if unhealthy == nil {
				unhealthy = map[transcodev1alpha1.Hardware]string{}
			}
			unhealthy[class] = msg
		}
		tiers := classTiers[class]
		l, err := task.ReadEncoderLimits(ctx, kv, string(class), now)
		if err != nil {
			continue // no report: the worker's own plan, which always has its device's, is what runs
		}
		d := transcodeplan.Device{Limits: make(map[transcode.Tier]transcode.Limits, len(tiers))}
		for _, tier := range tiers {
			d.Limits[tier] = l
		}
		if tier, err := task.ReadEncoderTier(ctx, kv, string(class), now); err == nil && slices.Contains(tiers, tier) {
			d.Tier = tier
		}
		devs[class] = d
	}
	return devs, unhealthy
}

// gpuNodes reports which GPU classes have a Ready, schedulable node that
// carries the class's GPU label as "true" and allocatable GPUs (spec
// §18.5): the label the class's pools are held to and the resource their
// pods request, so the node that makes admission choose a class is a node
// that class's pods can land on.
func (r *Reconciler) gpuNodes(ctx context.Context) (map[transcodev1alpha1.Hardware]bool, error) {
	var nodes corev1.NodeList
	if err := r.Client.List(ctx, &nodes); err != nil {
		return nil, fmt.Errorf("admission: list Nodes: %w", err)
	}
	out := map[transcodev1alpha1.Hardware]bool{}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		if n.Spec.Unschedulable || !nodeReady(n) {
			continue
		}
		for class, res := range pool.GPUResource {
			if n.Labels[r.Pool.NodeLabel(class)] != "true" {
				continue
			}
			if q, ok := n.Status.Allocatable[res]; ok && q.Sign() > 0 {
				out[class] = true
			}
		}
	}
	return out, nil
}

// nodeReady reports whether n's Ready condition is True.
func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// gpuNodeSignal is what of a Node a pass acts on: Ready,
// spec.unschedulable, the two GPU labels and the allocatable GPUs (§5.7).
func gpuNodeSignal(cfg pool.Config) func(client.Object) string {
	return func(o client.Object) string {
		n, ok := o.(*corev1.Node)
		if !ok {
			return ""
		}
		s := fmt.Sprintf("ready=%t unschedulable=%t", nodeReady(n), n.Spec.Unschedulable)
		for _, class := range GPUClasses {
			q := n.Status.Allocatable[pool.GPUResource[class]]
			s += fmt.Sprintf(" %s=%s/%s", class, n.Labels[cfg.NodeLabel(class)], q.String())
		}
		return s
	}
}

// SetupWithManager registers transcode-admission: its own wake channel (a
// slot or window place freed, a pool state changed), Nodes, TranscodeProfile
// generations, pool Job events and a graft Job's start, end or deletion (a
// graft slot freed), all into the one Request.
// MaxConcurrentReconciles is 1: a pass counts then grants.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	one := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{Request}
	})
	b := ctrl.NewControllerManagedBy(mgr).Named(ControllerName).
		WatchesRawSource(source.Channel(r.Ledger.wake, one)).
		Watches(&corev1.Node{}, one, builder.WithPredicates(k8s.StatusFieldChanged(gpuNodeSignal(r.Pool)))).
		Watches(&transcodev1alpha1.TranscodeProfile{}, one, builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&batchv1.Job{}, one, builder.WithPredicates(k8s.Or(pool.JobPredicate(),
			k8s.And(predicate.NewPredicateFuncs(isGraftJob), k8s.StatusFieldChanged(graftJobSignal)))))
	if r.VideoLease.Name != "" {
		// A count that rises or falls is a pass at once, not at the backstop.
		b = b.Watches(&coordinationv1.Lease{}, one, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
			return o.GetNamespace() == r.VideoLease.Namespace && o.GetName() == r.VideoLease.Name
		})))
	}
	return b.WithOptions(controller.Options{MaxConcurrentReconciles: 1, RecoverPanic: new(true)}).Complete(r)
}
