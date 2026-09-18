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

package search_test

import (
	"context"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// testCfg is the shared envtest control plane. One apiserver is started for
// the whole package rather than one per test: every test in this package uses
// its own namespace, so they do not interfere, and starting ~6 apiservers
// instead of one keeps `make test` from growing by minutes.
//
// It is nil when KUBEBUILDER_ASSETS is unset, in which case every envtest in
// this package skips -- a skip is NOT a pass, see the package's task report.
var testCfg *rest.Config

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run())
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		panic("start envtest: " + err.Error())
	}
	testCfg = cfg
	code := m.Run()
	if err := env.Stop(); err != nil {
		panic("stop envtest: " + err.Error())
	}
	os.Exit(code)
}

// newTestClient returns a client against the shared control plane, skipping
// when the envtest assets are absent.
func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	c, err := client.New(testCfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return c
}

// newNamespace creates ns, tolerating a re-run against the shared control
// plane.
func newNamespace(t *testing.T, ctx context.Context, c client.Client, ns string) {
	t.Helper()
	err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace %s: %v", ns, err)
	}
}
