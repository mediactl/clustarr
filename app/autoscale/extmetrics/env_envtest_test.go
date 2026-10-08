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

package extmetrics_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/mediactl/clustarr/app/autoscale/extmetrics"
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

func createNamespace(t *testing.T, c client.Client, name string) string {
	t.Helper()
	require.NoError(t, c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}))
	return name
}

// installerAPIService is v1beta1.external.metrics.k8s.io as the chart and
// kustomize render it (§10.2.2): no caBundle.
func installerAPIService(namespace, service string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiregistration.k8s.io/v1", "kind": "APIService",
		"metadata": map[string]any{"name": extmetrics.APIServiceName},
		"spec": map[string]any{
			"group": "external.metrics.k8s.io", "version": "v1beta1",
			"service":              map[string]any{"namespace": namespace, "name": service, "port": int64(443)},
			"groupPriorityMinimum": int64(100), "versionPriority": int64(100),
			"insecureSkipTLSVerify": false,
		},
	}}
}

func apiServiceKey() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(extmetrics.APIServiceGVK)
	u.SetName(extmetrics.APIServiceName)
	return u
}
