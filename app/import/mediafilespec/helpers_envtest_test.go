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

package mediafilespec_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// testClient talks straight to the apiserver: RenameFile takes a writer
// and an uncached reader, and nothing here needs a cache.
var testClient client.Client

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run()) // every envtest below skips itself
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
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

type fixture struct {
	ns, root string
	rf       *catalogv1alpha1.RootFolder
	c        client.Client
}

// newFixture keeps rescan's signature so the moved tests read unchanged;
// these tests make no LibraryScan, so the scan mode is unused.
func newFixture(t *testing.T, ctx context.Context, name string, kind catalogv1alpha1.RootFolderKind,
	profile string, _ catalogv1alpha1.ScanMode,
) *fixture {
	t.Helper()
	if testClient == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	require.NoError(t, testClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}))
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: name},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: mediaTempDir(t), Kind: kind,
			Defaults: catalogv1alpha1.RootDefaults{QualityProfileRef: profile},
		},
	}
	require.NoError(t, testClient.Create(ctx, rf))
	return &fixture{ns: name, root: rf.Spec.Path, rf: rf, c: testClient}
}

// api is the uncached reader RenameFile re-reads through.
func (f *fixture) api(t *testing.T) client.Reader {
	t.Helper()
	return f.c
}

// takeOver applies catalogarr's post-transcode take-over of size, mtime
// and original, as the MediaFile reconciler does after a swap.
func takeOver(ctx context.Context, c client.Client, ns, name string, size int64, mod time.Time) error {
	_, err := k8s.Apply(ctx, c, k8s.ManagerCatalogarr, catalogac.MediaFile(name, ns).WithSpec(
		catalogac.MediaFileSpec().WithSizeBytes(size).WithModTime(metav1.NewTime(mod)).WithOriginal(false)))
	return err
}

// mediaTempDir returns a fresh, empty directory to plant a test library in,
// removed when the test ends.
//
// It cannot be t.TempDir(): RootFolder.spec.path carries a CEL rule
// (`self.startsWith('/data/media/')`) that the envtest apiserver enforces, so
// a root folder pointed at /tmp is rejected before any of this code runs.
// /data is the RWX volume spec §11 mounts in every media-touching pod and is
// where the e2e suites plant files too.
//
// A writable media root is a prerequisite of this suite in exactly the way
// KUBEBUILDER_ASSETS is, and the ffprobe binary is elsewhere in the tree, so
// its absence is a named skip rather than a failure: a missing prerequisite
// and a broken scanner must not look the same in the output. `make test`
// creates the directory, and CLUSTARR_TEST_MEDIA_ROOT overrides it (the CEL
// rule means any override still has to start with /data/media/).
func mediaTempDir(t *testing.T) string {
	t.Helper()
	prefix := os.Getenv("CLUSTARR_TEST_MEDIA_ROOT")
	if prefix == "" {
		prefix = "/data/media"
	}
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Skipf("%s is not creatable (%v); RootFolder.spec.path must start with /data/media/, "+
			"so run `make test`, or `mkdir -p %s` by hand, or set CLUSTARR_TEST_MEDIA_ROOT "+
			"to a writable directory under /data/media/", prefix, err, prefix)
	}
	dir, err := os.MkdirTemp(prefix, "clustarr-mediafilespec-")
	if err != nil {
		t.Skipf("%s is not writable (%v); RootFolder.spec.path must start with /data/media/, "+
			"so run `make test`, or make %s writable, or set CLUSTARR_TEST_MEDIA_ROOT "+
			"to a writable directory under /data/media/", prefix, err, prefix)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// mustWriteFile creates a sparse file of the given size, so a test can plant
// a "60 MiB" movie without writing 60 MiB.
func mustWriteFile(t *testing.T, path string, size int64) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path) //nolint:gosec // a t.TempDir() path
	require.NoError(t, err)
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())
}

func splitPath(jsonPath string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(jsonPath); i++ {
		if i == len(jsonPath) || jsonPath[i] == '.' {
			out = append(out, jsonPath[start:i])
			start = i + 1
		}
	}
	return out
}

func ownsPath(fields map[string]any, parts []string) bool {
	if len(parts) == 0 {
		return true
	}
	next, ok := fields["f:"+parts[0]]
	if !ok {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	child, ok := next.(map[string]any)
	if !ok {
		return false
	}
	return ownsPath(child, parts[1:])
}
