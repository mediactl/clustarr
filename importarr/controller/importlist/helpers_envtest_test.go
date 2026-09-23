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
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// testClient is the envtest apiserver client every test in this package
// shares. It is nil when KUBEBUILDER_ASSETS is unset, which is how the
// tests know to skip.
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

// recvListTask subscribes to the import-list work queue and returns the
// first ListTask delivered, or fails the test after timeout.
func recvListTask(t *testing.T, ctx context.Context, bus events.Bus, timeout time.Duration) schema.ListTask {
	t.Helper()
	spec, ok := events.Default().Consumer(events.ConsumerImportList)
	require.True(t, ok, "events.ConsumerImportList must be in the default topology")

	received := make(chan schema.ListTask, 1)
	stop, err := bus.Subscribe(ctx, spec.Subscription(), func(_ context.Context, m events.Message) error {
		var task schema.ListTask
		if err := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &task); err != nil {
			return err
		}
		received <- task
		return nil
	})
	require.NoError(t, err)
	defer stop()

	select {
	case task := <-received:
		return task
	case <-time.After(timeout):
		t.Fatal("no ListTask received within timeout")
		return schema.ListTask{}
	}
}

// managerFor returns the field manager that owns the "status" subresource
// on obj, or "" when nobody owns it.
func managerFor(entries []metav1.ManagedFieldsEntry) string {
	for _, e := range entries {
		if e.Subresource == "status" {
			return e.Manager
		}
	}
	return ""
}
