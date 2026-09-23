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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
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

// The Z1 follow-up, against a steady-state engine workload: a rotated
// provider Secret rolls the usenet engine at the next reconcile -- the
// periodic one, five minutes at most, in production -- because the
// reconcile reads the Secret by name and its data digest is in the engine
// pod template's config hash. Nothing watches Secrets (the controller's
// ruling on cdde136), so the reconcile is what picks the rotation up. A
// change to the Secret's metadata alone restarts nothing.
func TestARotatedProviderSecretRollsTheUsenetEngineAtTheNextReconcile(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	r := newRolloutReconciler(c)

	creds := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "nntp-creds", Namespace: "default"},
		Data:       map[string][]byte{"username": []byte("alice"), "password": []byte("one")},
	}
	require.NoError(t, c.Create(ctx, creds))
	require.NoError(t, c.Create(ctx, usenetClientWithSecret("rotating", "nntp-creds")))

	// Steady state: two reconciles, one hash.
	reconcileOK(t, r, "default", "rotating")
	steady := deploymentHash(t, c, "rotating-engine")
	reconcileOK(t, r, "default", "rotating")
	require.Equal(t, steady, deploymentHash(t, c, "rotating-engine"), "the hash is not stable across reconciles")

	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(creds), creds))
	creds.Annotations = map[string]string{"note": "touched"}
	require.NoError(t, c.Update(ctx, creds))
	reconcileOK(t, r, "default", "rotating")
	assert.Equal(t, steady, deploymentHash(t, c, "rotating-engine"), "a metadata-only change restarted the engine")

	rotate(t, c, "nntp-creds", "two")
	reconcileOK(t, r, "default", "rotating")
	rotated := deploymentHash(t, c, "rotating-engine")
	assert.NotEqual(t, steady, rotated, "the reconcile after a rotation did not change the engine pod template")

	reconcileOK(t, r, "default", "rotating")
	assert.Equal(t, rotated, deploymentHash(t, c, "rotating-engine"), "the engine rolls once per rotation, not on every reconcile")
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
