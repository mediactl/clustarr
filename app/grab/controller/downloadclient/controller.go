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
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	appsv1ac "k8s.io/client-go/applyconfigurations/apps/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	cevents "github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/records"
)

// recheckInterval is how often a DownloadClient is re-reconciled with no spec
// change or child-workload event to prompt it -- the same invented, documented
// default app/catalog/controller/rootfolder uses for the same reason: DiskSpaceOK
// is a live filesystem fact that can change with nothing in the cluster telling
// this controller so.
const recheckInterval = 5 * time.Minute

// Reason tokens local to DownloadClient; see pkg/k8s.Reason* for the shared
// set this reuses.
const (
	// ReasonBelowMinFree means DataDir's free space is under MinFreeBytes.
	ReasonBelowMinFree = "BelowMinFreeBytes"
	// ReasonStatfsFailed means the DiskUsage probe itself returned an error.
	ReasonStatfsFailed = "StatfsFailed"
	// ReasonEngineNotReady means the owned workload has fewer ready replicas
	// than desired.
	ReasonEngineNotReady = "EngineNotReady"

	// ReasonInvalidSpec means the engine cannot be run as specified: a
	// scratch path or publish dir that is not under the data mount.
	ReasonInvalidSpec = "InvalidSpec"
)

// Reconciler stands up a DownloadClient's engine workload -- a StatefulSet for
// protocol=torrent, a Deployment for protocol=usenet -- and reports
// DiskSpaceOK, EngineReady and the Ready roll-up. Field manager: k8s.ManagerGrabarr
// only; see doc.go for why the engine's own field set (k8s.ManagerGrabarrEngine,
// on Download.status) is out of scope here.
type Reconciler struct {
	Client   client.Client
	Recorder events.EventRecorder

	// DataDir is the shared RWX volume every grabarr pod mounts at /data
	// (config/manager/grabarr.yaml). DiskSpaceOK statfs's it directly on the
	// CONTROLLER pod, which is why the controller Deployment mounts the same
	// claim the engines do rather than this reconciler needing to ask an
	// engine pod for its own view.
	DataDir string

	// ScratchDir is where a usenet engine's scratch volume is mounted inside
	// the pod. It has no bearing on DiskSpaceOK (that is always DataDir,
	// because DataDir is where the finished content ends up for both
	// protocols) -- it only sizes the usenet container's volume mount.
	ScratchDir string

	// EngineImage is the container image stamped onto the engine workload.
	// There is no default: guessing an image tag would silently run the wrong
	// engine. D2-8 reads CLUSTARR_ENGINE_IMAGE and passes it here.
	EngineImage string

	// DataClaimName is the PersistentVolumeClaim every grabarr pod mounts at
	// DataDir. The two installers do NOT agree on it: config/ names it
	// "clustarr-data" (DefaultDataClaimName, which NewReconciler sets), while
	// charts/clustarr names it "<release fullname>-data". app/grab/run.go
	// therefore always overwrites it from --data-claim ($CLUSTARR_DATA_CLAIM,
	// which the chart sets); this field once claimed both installers used
	// "clustarr-data", and under any release name but "clustarr" every
	// engine mounted a claim that did not exist.
	DataClaimName string

	// Engine is what every engine pod needs from this controller's own
	// process: its ServiceAccount, the bus, the umask (see EngineRuntime).
	// NewReconciler sets Engine.ServiceAccountName to
	// [DefaultEngineServiceAccount]; app/grab/run.go overwrites every field
	// from its options and environment.
	Engine EngineRuntime

	// MinFreeBytes is the floor DiskSpaceOK enforces on the engines' own
	// free bytes (their records'). Zero uses [DefaultMinFreeBytes].
	MinFreeBytes int64

	// SecretReader reads a provider or proxy Secret by name, for the
	// engine-config-hash: mgr.GetAPIReader() in production.
	SecretReader client.Reader

	// Bus publishes the resync and unidentified-removal commands; Admin
	// ensures and deletes the per-engine durables (ADR-0019 §5.1, §6.7).
	// The manager alone creates topology (§8.5).
	Bus   cevents.Bus
	Admin cevents.StreamAdmin

	// Engines reads the engines' records (clustarr-engines).
	Engines *records.Reader[*schema.EngineRecord]

	// EngineRecorder records the engines' Events the manager raises for
	// them: ProxyUDPUnavailable and UnidentifiedTransferRemoved, as
	// grabarr-engine (ADR-0019 §7.7). nil uses Recorder.
	EngineRecorder events.EventRecorder

	// Now is the clock; nil is time.Now.
	Now func() time.Time

	// UnidentifiedGate holds the removal of unidentified transfers; nil is
	// always open. Release N sets the adoption gate (A8.1, §10.2).
	UnidentifiedGate func(ctx context.Context, dc *downloadv1alpha1.DownloadClient) bool

	// The leader-local books: when an unidentified transfer was first seen,
	// which removals were already commanded, which durables were ensured,
	// and which clients owe a resync after a transfers-bucket loss.
	mu             sync.Mutex
	firstSeen      map[string]time.Time
	removalsIssued map[string]bool
	ensured        map[string]bool
	resyncWanted   map[types.NamespacedName]bool
}

const DefaultDataClaimName = "clustarr-data"

// NewReconciler builds a Reconciler with the real filesystem probe and the
// project's documented defaults.
func NewReconciler(c client.Client, recorder events.EventRecorder, dataDir, scratchDir, engineImage string) *Reconciler {
	return &Reconciler{
		Client:        c,
		Recorder:      recorder,
		DataDir:       dataDir,
		ScratchDir:    scratchDir,
		EngineImage:   engineImage,
		DataClaimName: DefaultDataClaimName,
		Engine:        EngineRuntime{ServiceAccountName: DefaultEngineServiceAccount},
		MinFreeBytes:  DefaultMinFreeBytes,
		SecretReader:  c,
	}
}

func (r *Reconciler) minFreeBytes() int64 {
	if r.MinFreeBytes > 0 {
		return r.MinFreeBytes
	}
	return DefaultMinFreeBytes
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "downloadclient.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("downloadclient", req.NamespacedName)

	var dc downloadv1alpha1.DownloadClient
	if err := r.Client.Get(ctx, req.NamespacedName, &dc); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		// The client is gone: its engines' durables go once no entry is
		// pinned to them (an entry drops EngineTeardownTimeout after its
		// engine is gone, §6.8), so come back while one still is.
		kept, err := r.sweepDurables(ctx, req.Namespace, req.Name, 0)
		if err != nil {
			return ctrl.Result{}, err
		}
		if kept > 0 {
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		return ctrl.Result{}, nil
	}

	ownerRef, err := k8s.OwnerReferenceAC(&dc, r.Client.Scheme())
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("downloadclient: owner reference: %w", err)
	}

	workloadName := engineWorkloadName(dc.Name)
	// A spec the engine could not run under (a working or publish directory
	// off the data mount) applies no workload at all: the condition below
	// says why, and the next spec change re-runs this. The old workload, if
	// any, is left as it was rather than rolled onto a bad template.
	var desiredReplicas, replicas, readyReplicas int32
	specErr := validateEngineDirs(&dc, r.DataDir)
	if specErr == nil {
		desiredReplicas, replicas, readyReplicas, err = r.reconcileWorkload(ctx, &dc, workloadName, ownerRef)
		if err != nil {
			log.Error("reconcile engine workload", "error", err)
			return ctrl.Result{}, err
		}
	} else {
		desiredReplicas = 1
	}

	now := r.now()
	// The per-engine durables (§5.1): one per rendered ordinal, ensured by
	// the manager; a scaled-away ordinal's goes once nothing is pinned to it.
	if err := r.reconcileDurables(ctx, &dc, desiredReplicas); err != nil {
		log.Error("reconcile engine durables", "error", err)
		return ctrl.Result{}, err
	}

	// Engine records instead of the Downloads (§6.7).
	es, err := r.engineRecords(ctx, &dc, desiredReplicas)
	if err != nil {
		log.Error("read engine records", "error", err)
		return ctrl.Result{}, err
	}

	// A resync after a transfers-bucket loss: a new resyncSeq, published to
	// every engine after the apply, again until each record reaches it.
	resyncSeq := dc.Status.ResyncSeq
	r.mu.Lock()
	wanted := r.resyncWanted[req.NamespacedName]
	r.mu.Unlock()
	if wanted {
		resyncSeq = records.NextSeq(dc.Status.ResyncSeq, 0, now)
	}

	conditions := append([]metav1.Condition(nil), dc.Status.Conditions...)
	freeOK := es.reported > 0 && es.freeBytes >= r.minFreeBytes()
	setDiskSpaceCondition(&dc, &conditions, freeOK, es.freeBytes, r.minFreeBytes(), es.freeErr())
	fresh := es.freshReady == desiredReplicas
	engineReady := specErr == nil && desiredReplicas > 0 && replicas == desiredReplicas && readyReplicas == desiredReplicas && fresh
	setEngineReadyCondition(&dc, &conditions, engineReady, replicas, readyReplicas, desiredReplicas, es.freshReady)
	r.setProxyCondition(&dc, &conditions, es.proxyUnavailable)
	ready := freeOK && engineReady
	reason, message := k8s.ReasonReconciled, "engine ready and disk space above minFreeBytes"
	if !ready {
		reason = ReasonEngineNotReady
		message = "engine or disk space not ready; see EngineReady and DiskSpaceOK"
		if !freeOK {
			reason = ReasonBelowMinFree
		}
		if specErr != nil {
			reason, message = ReasonInvalidSpec, specErr.Error()
		}
	}
	if k8s.MarkReady(&dc, &conditions, ready, reason, "%s", message) && !ready && r.Recorder != nil {
		r.Recorder.Eventf(&dc, nil, "Warning", reason, "Reconcile", message)
	}

	st := downloadac.DownloadClientStatus().
		WithObservedGeneration(dc.Generation).
		WithEngine(downloadac.EngineStatus().
			WithWorkloadRef(workloadName).
			WithReplicas(replicas).
			WithReadyReplicas(readyReplicas)).
		WithActive(es.active).
		WithQueued(es.queued).
		WithSeeding(es.seeding).
		WithFreeBytes(es.freeBytes).
		WithUnidentifiedTransfers(es.unidentified).
		WithConditions(k8s.ConditionACs(conditions)...)
	if resyncSeq != 0 {
		st = st.WithResyncSeq(resyncSeq)
	}
	ac := downloadac.DownloadClient(dc.Name, dc.Namespace).WithStatus(st)
	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerGrabarr, ac); err != nil {
		log.Error("patch status", "error", err)
		return ctrl.Result{}, err
	}
	if wanted {
		r.mu.Lock()
		delete(r.resyncWanted, req.NamespacedName)
		r.mu.Unlock()
	}

	// After the apply: the resync commands, then the unidentified removals.
	pending := r.publishResync(ctx, &dc, es, resyncSeq, now)
	r.removeUnidentified(ctx, &dc, es, now)
	if pending {
		return ctrl.Result{RequeueAfter: resyncRepublish}, nil
	}
	return ctrl.Result{RequeueAfter: recheckInterval}, nil
}

var recreateStrategyPatch = []byte(`{"spec":{"strategy":{"type":"Recreate","rollingUpdate":null}}}`)

// migrateToRecreate moves a usenet engine Deployment created before
// buildDeployment set the Recreate strategy onto it, once, ahead of the
// apply. Server-side apply cannot do this itself: the old Deployment carries
// the apiserver's defaulted rollingUpdate block, which no field manager owns
// and an apply therefore never removes, and the apiserver rejects a Recreate
// Deployment that still has one -- so the apply would fail on every reconcile
// from then on. A merge patch can say "rollingUpdate: null"; an apply
// configuration cannot. A Deployment that is absent or already Recreate is
// left alone, so this costs one cached Get in the steady state.
func (r *Reconciler) migrateToRecreate(ctx context.Context, namespace, name string) error {
	var live appsv1.Deployment
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &live); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("downloadclient: get Deployment %s: %w", name, err)
	}
	if live.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType {
		return nil
	}
	if err := r.Client.Patch(ctx, &live, client.RawPatch(types.MergePatchType, recreateStrategyPatch),
		client.FieldOwner(string(k8s.ManagerGrabarr))); err != nil {
		return fmt.Errorf("downloadclient: move Deployment %s to the Recreate strategy: %w", name, err)
	}
	return nil
}

// reconcileWorkload applies the StatefulSet (torrent) or Deployment (usenet)
// the DownloadClient describes and reads back its live replica counts. It
// returns the desired replica count alongside the two observed ones so the
// caller can compute EngineReady without a second round trip.
func (r *Reconciler) reconcileWorkload(
	ctx context.Context,
	dc *downloadv1alpha1.DownloadClient,
	workloadName string,
	ownerRef *metav1ac.OwnerReferenceApplyConfiguration,
) (desired, replicas, readyReplicas int32, err error) {
	switch dc.Spec.Protocol {
	case commonv1alpha1.ProtocolTorrent:
		// The proxy's Secret is read at start, like a usenet provider's, and
		// a read failure aborts the apply for the same reason as below.
		secrets, err := r.secretDigests(ctx, dc)
		if err != nil {
			return 0, 0, 0, err
		}
		if needsScratchClaim(dc) {
			pvc := buildScratchPVC(dc, ownerRef)
			if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerGrabarr, pvc); err != nil {
				return 0, 0, 0, fmt.Errorf("downloadclient: apply scratch PVC for %s: %w", dc.Name, err)
			}
		}
		sts := buildStatefulSet(dc, workloadName, r.EngineImage, r.DataDir, r.ScratchDir, r.DataClaimName, r.Engine, secrets, ownerRef)
		if err := r.replaceForClaimTemplates(ctx, dc.Namespace, workloadName, sts); err != nil {
			return 0, 0, 0, err
		}
		if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerGrabarr, sts); err != nil {
			return 0, 0, 0, fmt.Errorf("downloadclient: apply StatefulSet %s: %w", workloadName, err)
		}
		var live appsv1.StatefulSet
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: dc.Namespace, Name: workloadName}, &live); err != nil {
			if apierrors.IsNotFound(err) {
				return torrentReplicas(dc), 0, 0, nil
			}
			return 0, 0, 0, fmt.Errorf("downloadclient: get StatefulSet %s: %w", workloadName, err)
		}
		return torrentReplicas(dc), live.Status.Replicas, live.Status.ReadyReplicas, nil

	case commonv1alpha1.ProtocolUsenet:
		if needsScratchClaim(dc) {
			pvc := buildScratchPVC(dc, ownerRef)
			if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerGrabarr, pvc); err != nil {
				return 0, 0, 0, fmt.Errorf("downloadclient: apply scratch PVC for %s: %w", dc.Name, err)
			}
		}
		if err := r.migrateToRecreate(ctx, dc.Namespace, workloadName); err != nil {
			return 0, 0, 0, err
		}
		// Before the apply, and a read failure aborts it: applying without
		// the digests would change the hash and restart the engine for a
		// failed read rather than a rotated credential.
		secrets, err := r.secretDigests(ctx, dc)
		if err != nil {
			return 0, 0, 0, err
		}
		dep := buildDeployment(dc, workloadName, r.EngineImage, r.DataDir, r.ScratchDir, r.DataClaimName, r.Engine, secrets, ownerRef)
		if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerGrabarr, dep); err != nil {
			return 0, 0, 0, fmt.Errorf("downloadclient: apply Deployment %s: %w", workloadName, err)
		}
		var live appsv1.Deployment
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: dc.Namespace, Name: workloadName}, &live); err != nil {
			if apierrors.IsNotFound(err) {
				return 1, 0, 0, nil
			}
			return 0, 0, 0, fmt.Errorf("downloadclient: get Deployment %s: %w", workloadName, err)
		}
		return 1, live.Status.Replicas, live.Status.ReadyReplicas, nil

	default:
		// DownloadClientSpec's own CEL rule requires exactly one of torrent or
		// usenet, keyed off spec.protocol's enum, so this is unreachable
		// through the apiserver. It is left unhandled -- no workload applied,
		// no status touched -- rather than sending a partial status
		// declaration for a case that cannot legitimately arise: an apply
		// nobody ever needs to send cannot release a field, but one built out
		// of a defensive guess could.
		return 0, 0, 0, fmt.Errorf("downloadclient: unknown protocol %q", dc.Spec.Protocol)
	}
}

func setDiskSpaceCondition(dc *downloadv1alpha1.DownloadClient, conditions *[]metav1.Condition, ok bool, free, minFree int64, probeErr error) {
	if probeErr != nil {
		k8s.MarkFalse(dc, conditions, downloadv1alpha1.DownloadClientConditionDiskSpaceOK, ReasonStatfsFailed, "%s", probeErr.Error())
		return
	}
	if ok {
		k8s.MarkTrue(dc, conditions, downloadv1alpha1.DownloadClientConditionDiskSpaceOK, k8s.ReasonReconciled,
			"free=%d minFreeBytes=%d", free, minFree)
		return
	}
	k8s.MarkFalse(dc, conditions, downloadv1alpha1.DownloadClientConditionDiskSpaceOK, ReasonBelowMinFree,
		"free=%d minFreeBytes=%d", free, minFree)
}

func setEngineReadyCondition(dc *downloadv1alpha1.DownloadClient, conditions *[]metav1.Condition, ready bool, replicas, readyReplicas, desired, freshReady int32) {
	if ready {
		k8s.MarkTrue(dc, conditions, downloadv1alpha1.DownloadClientConditionEngineReady, k8s.ReasonReconciled,
			"%d/%d replicas ready, each reporting ready", readyReplicas, desired)
		return
	}
	k8s.MarkFalse(dc, conditions, downloadv1alpha1.DownloadClientConditionEngineReady, ReasonEngineNotReady,
		"%d/%d replicas exist, %d ready, %d reporting ready on a fresh engine record, want %d",
		replicas, desired, readyReplicas, freshReady, desired)
}

// secretDigests reads every Secret the usenet engine for dc reads at start --
// each provider's secretRef -- and returns a digest of each one's data, keyed
// by Secret name, for [engineConfigHash]. A rotated credential then changes
// the engine pod template and the Deployment restarts the engine, which is
// the only way a running engine sees it: it resolves the credentials once,
// when it builds its client.
//
// Each Secret is read by name with a plain `get` through
// [Reconciler.SecretReader], and nothing watches Secrets: a watch needs list
// and watch, which RBAC cannot narrow by name or label, so grabarr could
// enumerate every Secret in its scope -- other services' credentials
// included -- for the sake of restarting an engine sooner (the controller's
// ruling on cdde136). A rotation is therefore picked up by the periodic
// reconcile ([recheckInterval], five minutes) or by any earlier one, which
// re-reads the Secret and rolls the engine.
//
// A Secret that does not exist digests as "absent" -- the engine cannot start
// without it, and creating it must restart the engine, which a changed digest
// does. Any other read error is returned rather than digested: a digest for
// "could not read" would restart the engine on an apiserver blip, and again
// when the read recovered.
func (r *Reconciler) secretDigests(ctx context.Context, dc *downloadv1alpha1.DownloadClient) (map[string]string, error) {
	names := engineSecretNames(dc)
	if len(names) == 0 {
		return nil, nil
	}
	reader := r.SecretReader
	if reader == nil {
		reader = r.Client
	}
	out := make(map[string]string, len(names))
	for _, name := range names {
		if _, done := out[name]; done || name == "" {
			continue
		}
		var s corev1.Secret
		err := reader.Get(ctx, types.NamespacedName{Namespace: dc.Namespace, Name: name}, &s)
		switch {
		case apierrors.IsNotFound(err):
			out[name] = "absent"
		case err != nil:
			return nil, fmt.Errorf("downloadclient: read engine Secret %s/%s: %w", dc.Namespace, name, err)
		default:
			out[name] = secretDataDigest(s.Data)
		}
	}
	return out, nil
}

// engineSecretNames names every Secret dc's engine reads at start: each
// usenet provider's, or the torrent proxy's.
func engineSecretNames(dc *downloadv1alpha1.DownloadClient) []string {
	var names []string
	if dc.Spec.Usenet != nil {
		for _, p := range dc.Spec.Usenet.Providers {
			names = append(names, p.SecretRef.Name)
		}
	}
	if t := dc.Spec.Torrent; t != nil && t.Proxy != nil && t.Proxy.SecretRef != nil {
		names = append(names, t.Proxy.SecretRef.Name)
	}
	return names
}

// secretDataDigest hashes a Secret's data -- keys sorted, and every key and
// value length-prefixed so no two different maps encode alike. Only the
// digest reaches the pod template: the annotation is readable by anyone who
// can read the Deployment, so no credential, nor anything short enough to
// guess one from, is ever in it (it holds a truncated hash of all of these
// digests together).
func secretDataDigest(data map[string][]byte) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	var n [8]byte
	for _, k := range keys {
		binary.BigEndian.PutUint64(n[:], uint64(len(k)))
		h.Write(n[:])
		h.Write([]byte(k))
		binary.BigEndian.PutUint64(n[:], uint64(len(data[k])))
		h.Write(n[:])
		h.Write(data[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SetupWithManager registers the DownloadClient controller: the client,
// its workloads, and its engines' records (a records source on
// clustarr-engines, whose record's item is the DownloadClient), plus a
// second source on clustarr-transfers used only for its recreation signal,
// which starts a resync of every client (§6.7).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		Named("downloadclient").
		For(&downloadv1alpha1.DownloadClient{}, builder.WithPredicates(k8s.GenerationChanged())).
		Owns(&appsv1.StatefulSet{}).
		Owns(&appsv1.Deployment{})
	if r.Bus != nil {
		b = b.WatchesRawSource(EngineRecordSource(r.Bus)).
			WatchesRawSource(r.transfersRecreated(mgr.GetClient()))
	}
	return b.WithOptions(controller.Options{ReconciliationTimeout: 5 * time.Minute}).Complete(r)
}

// replaceForClaimTemplates deletes the torrent StatefulSet name, orphaning
// its pods, when the apply would change its volumeClaimTemplates, which the
// apiserver refuses to update in place (spec.torrent.scratch turned to or
// from a storage class, or resized). The apply that follows creates it
// again; the new StatefulSet adopts the running pods by their labels and
// rolls them, as `kubectl delete --cascade=orphan` then apply would.
func (r *Reconciler) replaceForClaimTemplates(ctx context.Context, namespace, name string, desired *appsv1ac.StatefulSetApplyConfiguration) error {
	var live appsv1.StatefulSet
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &live); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("downloadclient: get StatefulSet %s: %w", name, err)
	}
	if !claimTemplatesChanged(&live, desired) {
		return nil
	}
	logging.FromContext(ctx).Info("downloadclient: scratch claim template changed; replacing the StatefulSet, keeping its pods",
		"statefulset", name)
	if err := r.Client.Delete(ctx, &live, client.PropagationPolicy(metav1.DeletePropagationOrphan),
		client.Preconditions{UID: &live.UID}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("downloadclient: replace StatefulSet %s: %w", name, err)
	}
	return nil
}
