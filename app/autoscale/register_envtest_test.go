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

package autoscale_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/mediactl/clustarr/app/autoscale"
	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// stubAdmin is a StreamAdmin whose every consumer has no backlog. The
// embedded nil interface panics on any other method: --autoscale=false
// reaches only ConsumerState, through the queue gauge.
type stubAdmin struct{ events.StreamAdmin }

func (stubAdmin) ConsumerState(context.Context, string, string) (events.ConsumerState, error) {
	return events.ConsumerState{}, nil
}

// --autoscale=false (§9.4) deletes the HPAs a previous release's manager
// owned, once, and leaves every other HPA alone.
func TestRegisterWithAutoscaleOffDeletesTheManagedHPAs(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
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
	require.NoError(t, c.Create(ctx, hpa("agent-catalog", map[string]string{agentdomain.LabelDomain: agentdomain.Catalog})))
	require.NoError(t, c.Create(ctx, hpa("unrelated", nil)))

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: k8s.MustNewScheme(), Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
		Controller: config.Controller{SkipNameValidation: new(true)},
	})
	require.NoError(t, err)
	require.NoError(t, autoscale.Register(mgr, stubAdmin{}, autoscale.Options{Namespace: ns, BindAddress: "0"}))
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = mgr.Start(runCtx) }()

	require.Eventually(t, func() bool {
		err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "agent-catalog"}, &autoscalingv2.HorizontalPodAutoscaler{})
		return apierrors.IsNotFound(err)
	}, 30*time.Second, 100*time.Millisecond)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "unrelated"}, &autoscalingv2.HorizontalPodAutoscaler{}))
}
