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

package bundle_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/indexarr/bundle"
	"github.com/mediactl/clustarr/pkg/k8s"
)

var testClient client.Client

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run()) // every envtest below skips itself
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start envtest: %v\n", err)
		os.Exit(1)
	}
	code := func() int {
		c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
		if err != nil {
			fmt.Fprintf(os.Stderr, "build client: %v\n", err)
			return 1
		}
		testClient = c
		return m.Run()
	}()
	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stop envtest: %v\n", err)
	}
	os.Exit(code)
}

func requireEnvtest(t *testing.T) client.Client {
	t.Helper()
	if testClient == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	return testClient
}

// TestLoaderAppliesTheBundleAndLeavesTheOperatorsObjects is the loader's
// contract (X14 wires it behind indexarr's --cardigann-definitions-dir):
// every definition cardigann.LoadBundle accepts becomes a labelled
// IndexerDefinition under indexarr's field manager, a re-run is idempotent
// and carries a changed file through, a refused file is counted and
// skipped, and an IndexerDefinition of the same name that is not the
// bundle's -- the operator's -- is never overwritten.
func TestLoaderAppliesTheBundleAndLeavesTheOperatorsObjects(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()

	real1337x, err := os.ReadFile("../../testdata/cardigann/1337x.yml")
	require.NoError(t, err)
	fsys := fstest.MapFS{
		"1337x.yml":  {Data: real1337x},
		"broken.yml": {Data: []byte("id: broken\nnot: a definition\n")},
		"README.md":  {Data: []byte("ignored: not a definition file")},
	}
	l := &bundle.Loader{Client: c, Dir: "/bundle", FS: fsys}

	res, err := l.SyncOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, bundle.Result{Applied: 1, Refused: 1}, res)

	var got indexv1alpha1.IndexerDefinition
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "1337x"}, &got))
	require.Equal(t, "true", got.Labels[bundle.LabelBundled])
	require.Equal(t, string(real1337x), got.Spec.YAML)
	var managers []string
	for _, e := range got.ManagedFields {
		if e.Subresource == "" {
			managers = append(managers, e.Manager)
		}
	}
	require.Equal(t, []string{string(k8s.ManagerIndexarr)}, managers,
		"the bundle's spec must be owned by indexarr's field manager alone")

	// Steady state: idempotent, and a changed file carries through.
	res, err = l.SyncOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.Applied)
	changed := append([]byte(nil), real1337x...)
	changed = append(changed, []byte("\n# a later corpus revision\n")...)
	fsys["1337x.yml"] = &fstest.MapFile{Data: changed}
	_, err = l.SyncOnce(ctx)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "1337x"}, &got))
	require.Equal(t, string(changed), got.Spec.YAML, "a changed bundle file did not reach its IndexerDefinition")

	// The operator's same-named object is theirs.
	operators := &indexv1alpha1.IndexerDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "mine"},
		Spec:       indexv1alpha1.IndexerDefinitionSpec{YAML: "id: mine\n# the operator's own\n"},
	}
	require.NoError(t, c.Create(ctx, operators))
	mine := []byte(string(real1337x))
	mine = []byte("---\nid: mine\n" + string(mine[len("---\nid: 1337x\n"):]))
	res, err = (&bundle.Loader{Client: c, Dir: "/bundle", FS: fstest.MapFS{"mine.yml": {Data: mine}}}).SyncOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, bundle.Result{Kept: 1}, res)
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "mine"}, &got))
	require.Equal(t, operators.Spec.YAML, got.Spec.YAML, "the loader overwrote an IndexerDefinition that is not the bundle's")

	// A directory that cannot be read fails loudly.
	_, err = (&bundle.Loader{Client: c, Dir: t.TempDir() + "/missing"}).SyncOnce(ctx)
	require.Error(t, err)
}
