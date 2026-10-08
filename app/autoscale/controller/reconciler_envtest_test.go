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

package controller_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/mediactl/clustarr/app/autoscale/controller"
	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func startEnv(t *testing.T) (*rest.Config, client.Client) {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return cfg, c
}

func startReconciler(t *testing.T, cfg *rest.Config, r *controller.Reconciler) {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: new(true)},
	})
	require.NoError(t, err)
	r.Client = mgr.GetClient()
	require.NoError(t, r.SetupWithManager(mgr))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

// eventLog drains a FakeRecorder so a reconciler that warns on every pass
// never blocks on a full channel.
type eventLog struct {
	mu   sync.Mutex
	seen []string
}

func collect(t *testing.T, rec *k8sevents.FakeRecorder) *eventLog {
	l := &eventLog{}
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case e := <-rec.Events:
				l.mu.Lock()
				l.seen = append(l.seen, e)
				l.mu.Unlock()
			case <-stop:
				return
			}
		}
	}()
	t.Cleanup(func() { close(stop) })
	return l
}

func (l *eventLog) has(reason string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.seen {
		if strings.Contains(e, " "+reason+" ") {
			return true
		}
	}
	return false
}

var agentLabel = map[string]string{"clustarr.io/agent": "true"}

func makeNode(t *testing.T, c client.Client, name string, labels map[string]string, ready corev1.ConditionStatus,
	taints []corev1.Taint, unschedulable bool) {
	t.Helper()
	ctx := context.Background()
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec: corev1.NodeSpec{Unschedulable: unschedulable, Taints: taints}}
	require.NoError(t, c.Create(ctx, n))
	// The apiserver's NodeTaint admission adds node.kubernetes.io/not-ready
	// to every new Node, and envtest runs no node-lifecycle controller to
	// lift it once the Node reports Ready: put back the taints asked for.
	n.Spec.Taints = taints
	require.NoError(t, c.Update(ctx, n))
	now := metav1.Now()
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready, Reason: "Test",
		LastHeartbeatTime: now, LastTransitionTime: now}}
	n.Status.Allocatable = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"),
		corev1.ResourceMemory: resource.MustParse("8Gi"), corev1.ResourcePods: resource.MustParse("110")}
	require.NoError(t, c.Status().Update(ctx, n)) //nolint:forbidigo // simulating the kubelet
}

func deployment(ns, name, domain string, replicas *int32) *appsv1.Deployment {
	sel := map[string]string{"app": name}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns,
			Labels: map[string]string{agentdomain.LabelDomain: domain, "app.kubernetes.io/component": name}},
		Spec: appsv1.DeploymentSpec{
			Replicas: replicas,
			Selector: &metav1.LabelSelector{MatchLabels: sel},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: sel},
				Spec: corev1.PodSpec{NodeSelector: agentLabel,
					Containers: []corev1.Container{{Name: "agent", Image: "example.invalid/native:test"}}},
			},
		},
	}
}

func waitHPA(t *testing.T, c client.Client, ns, name string, ok func(*autoscalingv2.HorizontalPodAutoscaler) bool) *autoscalingv2.HorizontalPodAutoscaler {
	t.Helper()
	var got autoscalingv2.HorizontalPodAutoscaler
	require.Eventually(t, func() bool {
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
			return false
		}
		return ok(&got)
	}, 30*time.Second, 100*time.Millisecond, "HPA %s/%s never reached the expected shape", ns, name)
	return &got
}

func waitNoHPA(t *testing.T, c client.Client, ns, name string) {
	t.Helper()
	require.Eventually(t, func() bool {
		err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &autoscalingv2.HorizontalPodAutoscaler{})
		return apierrors.IsNotFound(err)
	}, 30*time.Second, 100*time.Millisecond, "HPA %s/%s was never deleted", ns, name)
}

func hpaTargets(h *autoscalingv2.HorizontalPodAutoscaler) map[string]string {
	out := map[string]string{}
	for _, m := range h.Spec.Metrics {
		out[m.External.Metric.Selector.MatchLabels["consumer"]] = m.External.Target.AverageValue.String()
	}
	return out
}

func TestReconcilerBuildsTheHPA(t *testing.T) {
	cfg, c := startEnv(t)
	ctx := context.Background()
	const ns = "clustarr-system"
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	makeNode(t, c, "n1", agentLabel, corev1.ConditionTrue, nil, false)
	makeNode(t, c, "n2", agentLabel, corev1.ConditionTrue, nil, false)
	makeNode(t, c, "n3-cordoned", agentLabel, corev1.ConditionTrue, nil, true)
	makeNode(t, c, "n4-notready", agentLabel, corev1.ConditionFalse, nil, false)
	makeNode(t, c, "n5-tainted", agentLabel, corev1.ConditionTrue,
		[]corev1.Taint{{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}}, false)
	makeNode(t, c, "n6-unlabelled", nil, corev1.ConditionTrue, nil, false)

	rec := k8sevents.NewFakeRecorder(1000)
	log := collect(t, rec)
	startReconciler(t, cfg, &controller.Reconciler{Recorder: rec, Namespace: ns})

	imp := deployment(ns, "agent-import", agentdomain.Import, nil)
	require.NoError(t, c.Create(ctx, imp))
	hpa := waitHPA(t, c, ns, "agent-import", func(h *autoscalingv2.HorizontalPodAutoscaler) bool { return h.Spec.MaxReplicas == 2 })
	assert.Equal(t, int32(0), *hpa.Spec.MinReplicas)
	assert.Equal(t, map[string]string{
		events.ConsumerImportScan: "4", events.ConsumerImportList: "2", events.ConsumerImportFile: "4",
		events.ConsumerImportRecycle: "1", events.ConsumerImportProbeHigh: "4", events.ConsumerImportProbeLow: "4",
	}, hpaTargets(hpa))
	assert.Equal(t, int32(0), *hpa.Spec.Behavior.ScaleUp.StabilizationWindowSeconds)
	assert.Equal(t, int32(300), *hpa.Spec.Behavior.ScaleDown.StabilizationWindowSeconds)
	ref := metav1.GetControllerOf(hpa)
	require.NotNil(t, ref)
	assert.Equal(t, imp.UID, ref.UID)
	assert.Nil(t, ref.BlockOwnerDeletion)
	require.NotEmpty(t, hpa.ManagedFields)
	for _, mf := range hpa.ManagedFields {
		assert.Equal(t, string(k8s.ManagerAutoscale), mf.Manager, "only clustarr-autoscale writes the HPA")
	}

	// While n1 and n2 both match: the throttled consumers' cap, not the
	// node count, holds caption at one replica (§9.1.1, OD46).
	t.Run("caption is throttled to one replica on two matching nodes", func(t *testing.T) {
		require.NoError(t, c.Create(ctx, deployment(ns, "agent-caption", agentdomain.Caption, nil)))
		h := waitHPA(t, c, ns, "agent-caption", func(*autoscalingv2.HorizontalPodAutoscaler) bool { return true })
		assert.Equal(t, int32(1), h.Spec.MaxReplicas)
		assert.Equal(t, map[string]string{events.ConsumerCaptionFetchHigh: "16", events.ConsumerCaptionFetchNormal: "16"}, hpaTargets(h))
	})

	t.Run("cordoning a node lowers maxReplicas", func(t *testing.T) {
		var n2 corev1.Node
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "n2"}, &n2))
		n2.Spec.Unschedulable = true
		require.NoError(t, c.Update(ctx, &n2))
		waitHPA(t, c, ns, "agent-import", func(h *autoscalingv2.HorizontalPodAutoscaler) bool { return h.Spec.MaxReplicas == 1 })
	})

	t.Run("an env override changes the target", func(t *testing.T) {
		var d appsv1.Deployment
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(imp), &d))
		d.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: events.SlotsEnv, Value: events.ConsumerImportScan + "=2"}}
		require.NoError(t, c.Update(ctx, &d))
		waitHPA(t, c, ns, "agent-import", func(h *autoscalingv2.HorizontalPodAutoscaler) bool {
			return hpaTargets(h)[events.ConsumerImportScan] == "2"
		})
	})

	t.Run("a Deployment at zero is woken before its HPA appears", func(t *testing.T) {
		m := deployment(ns, "markers", agentdomain.Markers, new(int32(0)))
		require.NoError(t, c.Create(ctx, m))
		h := waitHPA(t, c, ns, "markers", func(*autoscalingv2.HorizontalPodAutoscaler) bool { return true })
		assert.Equal(t, int32(1800), *h.Spec.Behavior.ScaleDown.StabilizationWindowSeconds)
		var got appsv1.Deployment
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(m), &got))
		assert.Equal(t, int32(1), *got.Spec.Replicas, "an HPA wakes only a workload it scaled to zero itself")
	})

	t.Run("the paused annotation deletes the HPA and leaves replicas alone", func(t *testing.T) {
		var d appsv1.Deployment
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "agent-caption"}, &d))
		d.Annotations = map[string]string{agentdomain.AnnotationPaused: "true"}
		require.NoError(t, c.Update(ctx, &d))
		waitNoHPA(t, c, ns, "agent-caption")
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(&d), &d))
		assert.Equal(t, int32(1), *d.Spec.Replicas)
	})

	t.Run("removing the domain label deletes the HPA", func(t *testing.T) {
		var d appsv1.Deployment
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(imp), &d))
		delete(d.Labels, agentdomain.LabelDomain)
		require.NoError(t, c.Update(ctx, &d))
		waitNoHPA(t, c, ns, "agent-import")
	})

	t.Run("an HPA of that name the manager does not own is left alone", func(t *testing.T) {
		foreign := &autoscalingv2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Name: "agent-foreign", Namespace: ns},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "agent-foreign"},
				MinReplicas:    new(int32(1)), MaxReplicas: 5,
			},
		}
		require.NoError(t, c.Create(ctx, foreign))
		require.NoError(t, c.Create(ctx, deployment(ns, "agent-foreign", agentdomain.Import, nil)))
		require.Eventually(t, func() bool { return log.has(controller.ReasonAutoscaleConflict) }, 30*time.Second, 100*time.Millisecond)
		var got autoscalingv2.HorizontalPodAutoscaler
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(foreign), &got))
		assert.Equal(t, int32(5), got.Spec.MaxReplicas)
	})

	t.Run("a Deployment in another namespace is not autoscaled", func(t *testing.T) {
		require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "elsewhere"}}))
		require.NoError(t, c.Create(ctx, deployment("elsewhere", "agent-import", agentdomain.Import, nil)))
		time.Sleep(2 * time.Second)
		err := c.Get(ctx, types.NamespacedName{Namespace: "elsewhere", Name: "agent-import"}, &autoscalingv2.HorizontalPodAutoscaler{})
		assert.True(t, apierrors.IsNotFound(err), "the External Metrics API serves only the manager's namespace")
	})
}

// --autoscale=false (§9.4) deletes every HPA in the namespace labelled with
// a domain once, and nothing else.
func TestDeleteManagedHPAs(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "autoscale-off"
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	hpa := func(name string, labels map[string]string) *autoscalingv2.HorizontalPodAutoscaler {
		return &autoscalingv2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: name},
				MinReplicas:    new(int32(1)), MaxReplicas: 2,
			},
		}
	}
	require.NoError(t, c.Create(ctx, hpa("agent-import", map[string]string{agentdomain.LabelDomain: agentdomain.Import})))
	require.NoError(t, c.Create(ctx, hpa("someone-elses", nil)))

	n, err := controller.DeleteManagedHPAs(ctx, c, c, ns)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	waitNoHPA(t, c, ns, "agent-import")
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "someone-elses"}, &autoscalingv2.HorizontalPodAutoscaler{}))
}
