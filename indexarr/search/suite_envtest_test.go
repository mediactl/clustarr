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

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// testCfg is the shared envtest control plane. It is nil when
// KUBEBUILDER_ASSETS is unset, in which case every envtest here SKIPS -- and
// a skip is not a pass. Server-side apply's RELEASE semantics exist only on a
// real apiserver: the fake client accepts an apply and keeps whatever the
// object already had, so these tests cannot be replaced by one.
var testCfg *rest.Config

func TestMain(m *testing.M) {
	code := func() int {
		if os.Getenv("KUBEBUILDER_ASSETS") == "" {
			return m.Run()
		}
		env := &envtest.Environment{
			CRDDirectoryPaths:     []string{"../../config/crd/bases"},
			ErrorIfCRDPathMissing: true,
		}
		cfg, err := env.Start()
		if err != nil {
			panic("start envtest: " + err.Error())
		}
		testCfg = cfg
		defer func() {
			if stopErr := env.Stop(); stopErr != nil {
				panic("stop envtest: " + stopErr.Error())
			}
		}()
		return m.Run()
	}()
	os.Exit(code)
}

func requireEnvtest(t *testing.T) client.Client {
	t.Helper()
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	c, err := client.New(testCfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

func newNamespace(t *testing.T, ctx context.Context, c client.Client, ns string) {
	t.Helper()
	err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace %s: %v", ns, err)
	}
}
