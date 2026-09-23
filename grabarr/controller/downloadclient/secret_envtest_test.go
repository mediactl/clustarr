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

package downloadclient_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/grabarr"
	"github.com/mediactl/clustarr/grabarr/controller/downloadclient"
	"github.com/mediactl/clustarr/pkg/fsops"
)

func usenetClientWithSecret(name, secret string) *downloadv1alpha1.DownloadClient {
	return &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolUsenet,
			Replicas: 1,
			Usenet: &downloadv1alpha1.UsenetSpec{
				Providers: []downloadv1alpha1.NNTPProvider{{
					Name: "primary", Host: "news.example.com",
					SecretRef: corev1.LocalObjectReference{Name: secret},
				}},
			},
		},
	}
}

func deploymentHash(t *testing.T, c client.Client, name string) string {
	t.Helper()
	return templateHash(t, c, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name}})
}

func rotate(t *testing.T, c client.Client, name, password string) {
	t.Helper()
	var s corev1.Secret
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, &s))
	s.Data["password"] = []byte(password)
	require.NoError(t, c.Update(context.Background(), &s))
}

// The Z1 follow-up, against a steady-state engine workload under a running
// manager wired exactly as grabarr's controller role is: the manager's own
// options (whose Secret cache holds only labelled Secrets), the uncached API
// reader for reading Secrets by name, and the controller's watches.
//
//   - Rotating a labelled provider Secret restarts the engine by itself: the
//     watch reconciles the client, and the pod-template hash changes. The
//     periodic reconcile is five minutes out, so only the watch can do it
//     inside this test's window.
//   - Rotating an unlabelled one does not reach the watch -- the cache never
//     holds it -- but the next reconcile, the periodic one in production,
//     reads it by name and rolls the engine then.
//   - A change to a Secret's metadata alone restarts nothing.
func TestARotatedProviderSecretRollsTheUsenetEngine(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })

	o := grabarr.DefaultOptions()
	o.Role = grabarr.RoleController
	o.LeaderElect = false
	mo := o.ManagerOptions()
	mo.Metrics = metricsserver.Options{BindAddress: "0"}
	mo.HealthProbeBindAddress = "0"
	mgr, err := ctrl.NewManager(cfg, mo)
	require.NoError(t, err)

	r := downloadclient.NewReconciler(mgr.GetClient(), events.NewFakeRecorder(100), "/data", "/scratch", "img")
	r.SecretReader = mgr.GetAPIReader()
	r.DiskUsage = func(string) (fsops.Usage, error) {
		return fsops.Usage{Total: 100 << 30, Free: 50 << 30, Available: 50 << 30}, nil
	}
	require.NoError(t, r.SetupWithManager(mgr))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = mgr.Start(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })

	c, err := client.New(cfg, client.Options{Scheme: mgr.GetScheme()})
	require.NoError(t, err)

	labelled := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "watched-creds", Namespace: "default",
			Labels: map[string]string{downloadv1alpha1.LabelWatch: downloadv1alpha1.LabelWatchValue},
		},
		Data: map[string][]byte{"username": []byte("alice"), "password": []byte("one")},
	}
	plain := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "plain-creds", Namespace: "default"},
		Data:       map[string][]byte{"username": []byte("bob"), "password": []byte("one")},
	}
	require.NoError(t, c.Create(ctx, labelled))
	require.NoError(t, c.Create(ctx, plain))
	require.NoError(t, c.Create(ctx, usenetClientWithSecret("watched", "watched-creds")))
	require.NoError(t, c.Create(ctx, usenetClientWithSecret("plain", "plain-creds")))

	var watchedHash, plainHash string
	require.Eventually(t, func() bool {
		var d appsv1.Deployment
		if c.Get(ctx, client.ObjectKey{Namespace: "default", Name: "watched-engine"}, &d) != nil {
			return false
		}
		if c.Get(ctx, client.ObjectKey{Namespace: "default", Name: "plain-engine"}, &d) != nil {
			return false
		}
		watchedHash = deploymentHash(t, c, "watched-engine")
		plainHash = deploymentHash(t, c, "plain-engine")
		return true
	}, 30*time.Second, 100*time.Millisecond, "the steady-state engine workloads never appeared")

	// Metadata alone: no roll.
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(labelled), labelled))
	labelled.Annotations = map[string]string{"note": "touched"}
	require.NoError(t, c.Update(ctx, labelled))
	time.Sleep(2 * time.Second)
	assert.Equal(t, watchedHash, deploymentHash(t, c, "watched-engine"), "a metadata-only change restarted the engine")

	// A labelled rotation rolls the engine through the watch alone.
	rotate(t, c, "watched-creds", "two")
	require.Eventually(t, func() bool {
		return deploymentHash(t, c, "watched-engine") != watchedHash
	}, 30*time.Second, 100*time.Millisecond, "rotating a watched provider Secret did not change the engine pod template")

	// An unlabelled rotation is invisible to the watch...
	rotate(t, c, "plain-creds", "two")
	time.Sleep(2 * time.Second)
	assert.Equal(t, plainHash, deploymentHash(t, c, "plain-engine"),
		"an unlabelled Secret reached the watch; the cache is not filtered to labelled Secrets")
	// ...and rolls the engine at the next reconcile, which reads it by name.
	reconcileOK(t, r, "default", "plain")
	assert.NotEqual(t, plainHash, deploymentHash(t, c, "plain-engine"),
		"the periodic reconcile did not pick up a rotated unlabelled Secret")
}

// A provider Secret created after its DownloadClient restarts the engine,
// which could not have started without it; and the digest never puts the
// credential itself in the pod template.
func TestAProviderSecretAppearingRollsTheEngine(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	r := newRolloutReconciler(c)

	require.NoError(t, c.Create(ctx, usenetClientWithSecret("late", "late-creds")))
	reconcileOK(t, r, "default", "late")
	before := deploymentHash(t, c, "late-engine")

	require.NoError(t, c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "late-creds", Namespace: "default"},
		Data:       map[string][]byte{"username": []byte("carol"), "password": []byte("hunter2")},
	}))
	reconcileOK(t, r, "default", "late")
	after := deploymentHash(t, c, "late-engine")
	assert.NotEqual(t, before, after)

	var d appsv1.Deployment
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "default", Name: "late-engine"}, &d))
	for k, v := range d.Spec.Template.Annotations {
		assert.NotContains(t, v, "hunter2", "annotation %s carries the credential", k)
	}
}
