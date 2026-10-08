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

package importlist_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	importlistctrl "github.com/mediactl/clustarr/app/import/controller/importlist"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	worker "github.com/mediactl/clustarr/app/import/worker/importlist"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// testClient is the envtest apiserver client every test in this package
// shares; testConfig is its config, for a test that runs a manager.
var (
	testClient client.Client
	testConfig *rest.Config
)

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run()) // every envtest below skips itself
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../../config/crd/bases"},
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
		testClient, testConfig = c, cfg
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

func createNamespace(t *testing.T, ctx context.Context, c client.Client, name string) string {
	t.Helper()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}))
	return name
}

func newBus(t *testing.T, ctx context.Context) events.Bus {
	t.Helper()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

// managerFor returns the field manager that owns the top-level "spec" or
// "status" field on obj, or "" when nobody owns it -- coarse enough for
// these tests, which only need to tell "import-worker" apart from
// "import" and "catalog".
func managerFor(entries []metav1.ManagedFieldsEntry, subresource string) string {
	for _, e := range entries {
		if e.Subresource == subresource {
			return e.Manager
		}
	}
	return ""
}

// syncSeq makes every sync's SyncedAt at least a second later than the
// last: the ImportList controller diffs only a snapshot newer than
// status.lastSyncAt, which round-trips in seconds, and these tests sync one
// list several times a second.
var syncSeq atomic.Int64

// syncList runs one sync of il end to end, as the import agent and the
// manager run it (ADR-0019 §7.6): the list worker under test fetches and
// reports a snapshot, and the ImportList controller (c, the real one) diffs
// and applies it. A recycle-files task the controller publishes runs
// through the agent's recycle consumer before its publish returns, so a
// removeAndDelete has moved the files by the time the reconcile ends.
func syncList(t *testing.T, ctx context.Context, c client.Client, bus events.Bus, w *worker.Worker, il *catalogv1alpha1.ImportList) {
	t.Helper()
	at := time.Now().UTC().Add(time.Duration(syncSeq.Add(1)) * time.Second)
	w.Clock = func() time.Time { return at }
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, il.Namespace, il.Name, string(il.UID))))
	r := &importlistctrl.Reconciler{
		Client: c, APIReader: c, HTTPClient: w.HTTPClient, TraktBaseURL: w.TraktBaseURL,
		Bus: agentBus{Bus: bus, recycle: fileimport.RecycleHandler(fileimport.NewRecycleSweeper(c), fileimport.NewRecycleFiles(c))},
	}
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(il)})
	require.NoError(t, err)
}

// lastResult is il's status after its last sync: the counts and lastError
// the ImportList controller computed from the agent's snapshot (A6.2: the
// worker's own Result checkpoint retired).
func lastResult(t *testing.T, ctx context.Context, c client.Client, il *catalogv1alpha1.ImportList) catalogv1alpha1.ImportListStatus {
	t.Helper()
	var got catalogv1alpha1.ImportList
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(il), &got))
	return got.Status
}

// agentBus hands every recycle-files task published on it to the import
// agent's recycle consumer once the publish lands.
type agentBus struct {
	events.Bus
	recycle events.Handler
}

func (b agentBus) Publish(ctx context.Context, subject string, e *events.Envelope, opts ...events.PublishOption) (events.Receipt, error) {
	r, err := b.Bus.Publish(ctx, subject, e, opts...)
	if err != nil || e.Schema != (schema.RecycleFilesTask{}).Schema() {
		return r, err
	}
	return r, b.recycle(ctx, &fakeMessage{env: e, attempt: 1})
}
