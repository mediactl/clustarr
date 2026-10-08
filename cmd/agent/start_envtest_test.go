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

package main

import (
	"context"
	"io"
	"maps"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/facade"
	"github.com/mediactl/clustarr/internal/cli/agent"
	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/presence"
	"github.com/mediactl/clustarr/test/starttest"
)

// subscriptions records what a domain subscribes and serves through the bus
// the agent hands it (Options.WrapBus).
type subscriptions struct {
	events.Bus
	mu      sync.Mutex
	durable map[string]bool
	served  map[string]bool
}

func (s *subscriptions) Subscribe(ctx context.Context, sub events.Subscription, h events.Handler) (func(), error) {
	s.mu.Lock()
	s.durable[sub.Durable] = true
	s.mu.Unlock()
	return s.Bus.Subscribe(ctx, sub, h)
}

// Serve is events.Bus's responder method, recorded.
func (s *subscriptions) Serve(subject, queue string, h func(ctx context.Context, data []byte) ([]byte, error)) (func(), error) {
	s.mu.Lock()
	s.served[subject] = true
	s.mu.Unlock()
	return s.Bus.Serve(subject, queue, h)
}

// controllerNames is every controller this process has registered, by the
// controller-runtime metric each one sets at construction.
func controllerNames(t *testing.T) map[string]bool {
	t.Helper()
	families, err := ctrlmetrics.Registry.Gather()
	require.NoError(t, err)
	out := map[string]bool{}
	for _, f := range families {
		if f.GetName() != "controller_runtime_max_concurrent_reconciles" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "controller" {
					out[l.GetValue()] = true
				}
			}
		}
	}
	return out
}

// embeddedNATS is starttest.EmbeddedNATS keeping the server, so the test can
// see which connections are still open once an agent has stopped.
func embeddedNATS(t *testing.T) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-agent-start", Host: "127.0.0.1", Port: -1,
		JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second), "embedded NATS server did not become ready")
	t.Cleanup(srv.Shutdown)
	return srv
}

// openConnections is the names of the clients connected to srv.
func openConnections(t *testing.T, srv *natsserver.Server) []string {
	t.Helper()
	cz, err := srv.Connz(&natsserver.ConnzOptions{State: natsserver.ConnOpen})
	require.NoError(t, err)
	var names []string
	for _, c := range cz.Conns {
		names = append(names, c.Name)
	}
	return names
}

// ensureTopology does the manager's part at start: the single-node default
// topology on the server at natsURL, which an agent only waits for.
func ensureTopology(t *testing.T, natsURL string) {
	t.Helper()
	bus, nc, err := busconn.Connect(natsURL, "test-topology")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	defer func() { _ = busconn.Shutdown(ctx, bus, nc) }()
	require.NoError(t, busconn.EnsureTopology(ctx, bus, events.Default().ForSingleNode()))
}

// prepareTorrentClient creates the DownloadClient "torrents" the torrent
// engine serves as "torrents-0": an ephemeral port, no DHT (no egress here).
func prepareTorrentClient(t *testing.T, c client.Client) {
	t.Helper()
	_, portStr, err := net.SplitHostPort(starttest.FreeAddress(t))
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	require.NoError(t, c.Create(context.Background(), &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "torrents", Namespace: "default"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolTorrent,
			Torrent:  &downloadv1alpha1.TorrentSpec{ListenPort: int32(port), EnableDHT: new(false)}, //nolint:gosec // an ephemeral port
		},
	}))
}

// prepareUsenetClient creates the DownloadClient "sabnzbd" the usenet engine
// serves as "sabnzbd-0". Its provider is never dialled: the pool is built
// lazily and nothing adds a job.
func prepareUsenetClient(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sabnzbd-creds", Namespace: "default"},
		Data:       map[string][]byte{"username": []byte("u"), "password": []byte("p")},
	}))
	require.NoError(t, c.Create(ctx, &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "sabnzbd", Namespace: "default"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolUsenet,
			Replicas: 1,
			Usenet: &downloadv1alpha1.UsenetSpec{Providers: []downloadv1alpha1.NNTPProvider{{
				Name: "stub", Host: "127.0.0.1", SecretRef: corev1.LocalObjectReference{Name: "sabnzbd-creds"},
			}}},
		},
	}))
}

// httpGet is GET url's status and body, 0 when nothing answers.
func httpGet(t *testing.T, url string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(b)
}

// verifyFacade: the index domain's Torznab facade, under the agent-index
// identity, reads its key Secret (the manager creates it; the test did) and
// fails closed -- 401 without the key, an RSS document with it.
func verifyFacade(t *testing.T, addr, key string) {
	t.Helper()
	starttest.WaitFor(t, "the facade to load its key", func() bool {
		code, _ := httpGet(t, "http://"+addr+"/search/api?t=search&q=probe")
		return code == http.StatusUnauthorized
	})
	code, body := httpGet(t, "http://"+addr+"/search/api?t=search&q=probe&apikey="+key)
	assert.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, "<rss", "an authenticated aggregate search answers an RSS document")
}

// TestEveryDomainStartsServesProbesAndStopsOnSignal (§10.3.3): every domain
// starts under its own identity's generated role (the case would never go
// Ready, or fail its verify, on a grant its role lacks), goes live and
// ready, subscribes exactly its pkg/agentdomain durables and serves exactly
// its RPC subjects (a min-0 domain serves none: zero replicas would be an
// outage, §9.9), registers no controller (agents execute; the manager
// reconciles), and stops cleanly when its context ends -- the signal path
// through cli.Main. The stop holds ADR-0020 §1's order as built: the
// runnables stop while the connection is still up (the presence writer
// withdraws its key through it), and once Run returns the responders are
// gone and the connection is closed (busconn.ShutdownAfter: the bus
// drained, then the connection).
func TestEveryDomainStartsServesProbesAndStopsOnSignal(t *testing.T) {
	requireNative(t)
	env := starttest.StartEnv(t)
	srv := embeddedNATS(t)
	natsURL := srv.ClientURL()
	ensureTopology(t, natsURL)
	admin, err := client.New(env.Config, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	identity := map[string]string{
		"catalog": "agent-catalog", "events": "agent-events", "metadata": "agent-metadata",
		"import": "agent-import", "index": "agent-index", "caption": "agent-caption",
		"torrent-engine": "grab-engine", "usenet-engine": "grab-engine",
	}
	var identities []string
	for _, id := range identity {
		identities = append(identities, id)
	}
	kcs := starttest.IdentityKubeconfigs(t, env, identities)
	bus, nc, err := busconn.Connect(natsURL, "test-observer")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busconn.Shutdown(context.Background(), bus, nc) })

	const facadeKey = "start-envtest-facade-key"
	var facadeAddr string
	type engine struct {
		client  string
		ordinal int32
	}
	cases := []struct {
		domain  string
		engine  *engine // the engine identity the case serves
		prepare func(t *testing.T, o *agent.Options)
		verify  func(t *testing.T)
	}{
		{domain: "catalog"},
		{domain: "events"},
		{domain: "metadata"},
		{domain: "import", prepare: func(t *testing.T, o *agent.Options) { o.DataDir = t.TempDir() }},
		{domain: "index", prepare: func(t *testing.T, o *agent.Options) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			o.IndexPath = filepath.Join(t.TempDir(), "releases.db")
			facadeAddr = starttest.FreeAddress(t)
			o.FacadeBindAddress = facadeAddr
			require.NoError(t, admin.Create(context.Background(), &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: o.FacadeAPIKeySecret, Namespace: "default"},
				Data:       map[string][]byte{facade.APIKeyField: []byte(facadeKey)},
			}))
		}, verify: func(t *testing.T) { verifyFacade(t, facadeAddr, facadeKey) }},
		{domain: "caption", prepare: func(t *testing.T, o *agent.Options) { o.DataDir = t.TempDir() }},
		{domain: "torrent-engine", engine: &engine{"torrents", 0}, prepare: func(t *testing.T, o *agent.Options) {
			prepareTorrentClient(t, admin)
			o.Engine, o.DataDir = "torrents-0", t.TempDir()
		}},
		{domain: "usenet-engine", engine: &engine{"sabnzbd", 0}, prepare: func(t *testing.T, o *agent.Options) {
			prepareUsenetClient(t, admin)
			o.Engine, o.DataDir, o.ScratchDir = "sabnzbd-0", t.TempDir(), t.TempDir()
		}},
	}
	var covered []string
	for _, tc := range cases {
		covered = append(covered, tc.domain)
	}
	require.ElementsMatch(t, agent.Domains(), covered, "every domain has a case")

	for _, tc := range cases {
		t.Run(tc.domain, func(t *testing.T) {
			home, ok := agentdomain.Lookup(tc.domain)
			require.True(t, ok)
			pod := "agent-" + tc.domain + "-0"
			t.Setenv("POD_NAME", pod)

			o := agent.DefaultOptions()
			o.Domain, o.Namespace, o.NATSURL = tc.domain, "default", natsURL
			o.SkipNameValidation = true
			o.MetricsBindAddress = k8s.DisabledBindAddress
			o.HealthProbeBindAddress = starttest.FreeAddress(t)
			o.GracefulShutdownTimeout, o.TopologyWait = 10*time.Second, 10*time.Second
			o.RESTConfig = starttest.RESTConfig(t, kcs[identity[tc.domain]])
			rec := &subscriptions{durable: map[string]bool{}, served: map[string]bool{}}
			o.WrapBus = func(b events.Bus) events.Bus { rec.Bus = b; return rec }
			if tc.prepare != nil {
				tc.prepare(t, &o)
			}
			before := controllerNames(t)

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- agent.Run(ctx, o) }()
			t.Cleanup(cancel)
			for _, probe := range []string{"/healthz", "/readyz"} {
				starttest.WaitFor(t, tc.domain+probe, func() bool {
					select {
					case err := <-done:
						t.Fatalf("agent --domain=%s returned before %s answered: %v", tc.domain, probe, err)
					default:
					}
					code, _ := httpGet(t, "http://"+o.HealthProbeBindAddress+probe)
					return code == http.StatusOK
				})
			}
			if tc.verify != nil {
				tc.verify(t)
			}

			// A domain subscribes and serves from its runnables, which
			// start beside the readiness checks, not before them.
			wantDurables := map[string]bool{}
			for _, c := range home.Consumers {
				wantDurables[c] = true
			}
			if tc.engine != nil {
				wantDurables[events.EngineConsumerName(tc.engine.client, tc.engine.ordinal)] = true
			}
			wantServed := map[string]bool{}
			for _, s := range home.Serves {
				wantServed[s] = true
			}
			bound := func() (map[string]bool, map[string]bool) {
				rec.mu.Lock()
				defer rec.mu.Unlock()
				return maps.Clone(rec.durable), maps.Clone(rec.served)
			}
			deadline := time.Now().Add(30 * time.Second)
			for {
				durable, served := bound()
				if maps.Equal(durable, wantDurables) && maps.Equal(served, wantServed) || time.Now().After(deadline) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			durable, served := bound()
			assert.Equal(t, wantDurables, durable, "%s subscribes exactly its pkg/agentdomain durables (an engine: its own)", tc.domain)
			assert.Equal(t, wantServed, served, "%s serves exactly its pkg/agentdomain RPC subjects", tc.domain)
			if home.Autoscaled && home.MinReplicas == 0 {
				assert.Empty(t, served, "a min-0 domain serves no RPC: zero replicas would be an outage (§9.9)")
			}

			cancel()
			select {
			case err := <-done:
				require.NoError(t, err, "Run on a clean shutdown")
			case <-time.After(30 * time.Second):
				t.Fatal("Run did not return within 30s of its context ending")
			}
			// The client has closed when Run returns; the server notices on
			// its read loop, a moment later.
			agentConns := func() []string {
				var out []string
				for _, name := range openConnections(t, srv) {
					if strings.HasPrefix(name, "clustarr-agent-"+tc.domain+"@") {
						out = append(out, name)
					}
				}
				return out
			}
			for end := time.Now().Add(5 * time.Second); len(agentConns()) > 0 && time.Now().Before(end); {
				time.Sleep(50 * time.Millisecond)
			}
			assert.Empty(t, agentConns(), "%s left its NATS connection open after Run returned: the bus shutdown (ADR-0020 §1) never ran", tc.domain)
			left, err := (&presence.Reader{KV: bus.KV(events.BucketProgress)}).Domain(context.Background(), tc.domain)
			require.NoError(t, err)
			assert.Empty(t, left, "%s's presence writer stopped while its connection was still up", tc.domain)
			for subject := range wantServed {
				rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := bus.Request(rctx, subject, struct{}{}, &struct{}{})
				rcancel()
				assert.ErrorIs(t, err, events.ErrNoResponders, "%s still answers %s after Run returned", tc.domain, subject)
			}

			added := map[string]bool{}
			for n := range controllerNames(t) {
				if !before[n] {
					added[n] = true
				}
			}
			assert.Empty(t, added, "an agent registers no controller: the manager reconciles (§10.3.2)")
		})
	}
}
