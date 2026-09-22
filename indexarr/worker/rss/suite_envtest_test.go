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

package rss_test

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// testCfg is the shared envtest control plane. It is nil when
// KUBEBUILDER_ASSETS is unset, in which case every envtest here SKIPS -- and
// a skip is not a pass.
var testCfg *rest.Config

func TestMain(m *testing.M) {
	// A real TracerProvider, exporting nowhere. Without one the global
	// tracer is a no-op, Start returns an invalid span context and
	// tracing.Inject writes no traceparent -- so the assertion that the
	// firehose carries a trace would pass or fail for the wrong reason.
	shutdown, err := tracing.Setup(context.Background(), tracing.Options{
		Enabled: false, ServiceName: "indexarr-rss-test", SampleRatio: 1,
	})
	if err != nil {
		panic("tracing setup: " + err.Error())
	}

	code := func() int {
		defer func() { _ = shutdown(context.Background()) }()

		if os.Getenv("KUBEBUILDER_ASSETS") == "" {
			return m.Run()
		}
		env := &envtest.Environment{
			CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
			ErrorIfCRDPathMissing: true,
		}
		cfg, startErr := env.Start()
		if startErr != nil {
			panic("start envtest: " + startErr.Error())
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

func requireEnvtest(t *testing.T) {
	t.Helper()
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
}

// startSpan opens a span on a live provider, so tracing.Inject has a trace to
// carry onto the envelope.
func startSpan(t *testing.T) (context.Context, trace.Span) {
	t.Helper()
	return tracing.Start(t.Context(), "test.producer")
}

// setup returns a direct (uncached) client and a fresh namespace. The worker
// reads one Indexer by name, so a live cache buys nothing here and only adds
// a sync race between the test's Create and the worker's Get.
func setup(t *testing.T) (context.Context, client.Client, string) {
	t.Helper()
	requireEnvtest(t)

	c, err := client.New(testCfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)

	ctx := t.Context()
	return ctx, c, newNamespace(t, ctx, c)
}

var nsSeq int

func newNamespace(t *testing.T, ctx context.Context, c client.Client) string {
	t.Helper()
	nsSeq++
	ns := "rsswork-" + strconv.Itoa(nsSeq)
	err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace %s: %v", ns, err)
	}
	return ns
}

// newIndexer creates a plain enabled generic torrent Indexer.
func newIndexer(t *testing.T, ctx context.Context, c client.Client, ns, name string) *indexv1alpha1.Indexer {
	t.Helper()
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: "http://fixture.invalid",
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1.ProtocolTorrent},
		},
	}
	require.NoError(t, c.Create(ctx, idx))
	return idx
}

func getStatus(t *testing.T, ctx context.Context, c client.Client, ns, name string) indexv1alpha1.IndexerStatus {
	t.Helper()
	var idx indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &idx))
	return idx.Status
}

func getIndexer(t *testing.T, ctx context.Context, c client.Client, ns, name string) *indexv1alpha1.Indexer {
	t.Helper()
	var idx indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &idx))
	return &idx
}
