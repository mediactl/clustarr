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

package agent

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/presence"
	"github.com/mediactl/clustarr/test/starttest"
)

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

// runEnv is one control plane and one JetStream server with the topology,
// shared by a test's domains.
type runEnv struct {
	cfg     *rest.Config
	natsURL string
}

func newRunEnv(t *testing.T) runEnv {
	t.Helper()
	env := starttest.StartEnv(t)
	createEngineClients(t, env.Config)
	natsURL := starttest.EmbeddedNATS(t)
	ensureTopology(t, natsURL)
	return runEnv{cfg: env.Config, natsURL: natsURL}
}

// runOptions is domainOptions for a real Run against e, serving probes.
func (e runEnv) runOptions(t *testing.T, d string) Options {
	t.Helper()
	if d == "import" || d == "caption" {
		requireFFmpeg(t)
	}
	o := domainOptions(t, d)
	o.NATSURL, o.RESTConfig = e.natsURL, e.cfg
	o.HealthProbeBindAddress = starttest.FreeAddress(t)
	o.GracefulShutdownTimeout, o.TopologyWait = 10*time.Second, 10*time.Second
	return o
}

// running is one Run in progress.
type running struct {
	cancel context.CancelFunc
	done   chan error
}

// start runs Run(o) until the test ends and waits for its /readyz; a Run
// that fails first fails the test with its error.
func start(t *testing.T, o Options) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- Run(ctx, o) }()
	t.Cleanup(func() { _ = r.stop(t) })
	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case err := <-r.done:
			r.done <- err
			t.Fatalf("agent --domain=%s returned before it was ready: %v", o.Domain, err)
		default:
		}
		if code, _ := get(t, "http://"+o.HealthProbeBindAddress+"/readyz"); code == http.StatusOK {
			return r
		}
		if time.Now().After(deadline) {
			_, body := get(t, "http://"+o.HealthProbeBindAddress+"/readyz?verbose")
			t.Fatalf("agent --domain=%s not ready within 60s: %s", o.Domain, body)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// stop cancels the run, as SIGTERM does through cli.Main, and returns Run's
// result.
func (r *running) stop(t *testing.T) error {
	t.Helper()
	r.cancel()
	select {
	case err := <-r.done:
		r.done <- err
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return within 30s of its context ending")
		return nil
	}
}

// get is GET url's status and body, 0 when nothing answers.
func get(t *testing.T, url string) (int, string) {
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

// checkNames is the set of checks a verbose healthz or readyz answer lists.
func checkNames(body string) map[string]bool {
	out := map[string]bool{}
	for line := range strings.SplitSeq(body, "\n") {
		if name, ok := strings.CutPrefix(line, "[+]"); ok {
			out[strings.Fields(name)[0]] = true
		} else if name, ok := strings.CutPrefix(line, "[-]"); ok {
			out[strings.Fields(name)[0]] = true
		}
	}
	return out
}

// TestTheBusLivenessCheckIsRegistered (split §3.3 as amended 2026-10-07,
// S1): every domain's liveness is exactly ping and bus -- handlers that
// ignore their context have held a subscription's every slot past its
// budget, which only a restart cures -- plus ffgo where the domain runs
// FFmpeg (import's probe, caption's extraction).
func TestTheBusLivenessCheckIsRegistered(t *testing.T) {
	e := newRunEnv(t)
	for _, d := range Domains() {
		t.Run(d, func(t *testing.T) {
			o := e.runOptions(t, d)
			r := start(t, o)
			code, body := get(t, "http://"+o.HealthProbeBindAddress+"/healthz?verbose")
			assert.Equal(t, http.StatusOK, code, body)
			want := map[string]bool{"ping": true, "bus": true}
			if d == agentdomain.Import || d == agentdomain.Caption {
				want["ffgo"] = true
			}
			assert.Equal(t, want, checkNames(body), "%s's liveness checks", d)
			require.NoError(t, r.stop(t))
		})
	}
}

// TestEveryAgentDomainWritesPresence (ADR-0019 §5.3, ruling R14): every
// domain's run reports its pod in clustarr-progress -- its node, version,
// the durables it binds with their slots (none for the engines, whose
// engine record is their capacity report) -- for the manager's admission,
// and withdraws it when it stops, before the bus shuts down.
func TestEveryAgentDomainWritesPresence(t *testing.T) {
	e := newRunEnv(t)
	bus, nc, err := busconn.Connect(e.natsURL, "test-presence")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busconn.Shutdown(context.Background(), bus, nc) })
	kv := bus.KV(events.BucketProgress)
	read := func(t *testing.T, d string) []string {
		t.Helper()
		got, err := (&presence.Reader{KV: kv}).Domain(context.Background(), d)
		require.NoError(t, err)
		var pods []string
		for _, p := range got {
			pods = append(pods, p.Pod)
		}
		return pods
	}
	for _, d := range Domains() {
		t.Run(d, func(t *testing.T) {
			pod := "agent-" + d + "-5d8f7c9b4-q2w7x"
			t.Setenv("POD_NAME", pod)
			t.Setenv("NODE_NAME", "node-a")
			o := e.runOptions(t, d)
			r := start(t, o)

			var got []schema.AgentPresence
			starttest.WaitFor(t, d+"'s presence", func() bool {
				got, err = (&presence.Reader{KV: kv}).Domain(context.Background(), d)
				return err == nil && len(got) == 1
			})
			p := got[0]
			assert.Equal(t, d, p.Domain)
			assert.Equal(t, pod, p.Pod)
			assert.Equal(t, "node-a", p.Node)
			assert.NotEmpty(t, p.Version)
			home, ok := agentdomain.Lookup(d)
			require.True(t, ok)
			assert.ElementsMatch(t, home.Consumers, p.Durables, "%s binds its table's durables", d)
			for _, c := range home.Consumers {
				assert.Positive(t, p.Slots[c], "%s's slots for %s", d, c)
			}
			if d == agentdomain.Import || d == agentdomain.Caption {
				assert.Equal(t, "9", p.Capabilities["ffmpeg"], "%s proves FFmpeg 9", d)
			}

			require.NoError(t, r.stop(t))
			assert.Empty(t, read(t, d), "%s withdraws its presence when it stops", d)
		})
	}
}

// TestAgentClientIsReadOnly (ADR-0019 §9.2): the manager an agent hands its
// domain reads through, refuses every write with k8s.ErrAgentWrite -- even
// under a role that would allow it (this run is cluster-admin) -- and
// records Events nowhere.
func TestAgentClientIsReadOnly(t *testing.T) {
	e := newRunEnv(t)
	admin, err := client.New(e.cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	ctx := context.Background()
	target := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "agent-readonly-target", Namespace: "default"}}
	require.NoError(t, admin.Create(ctx, target))

	const d = agentdomain.Events
	var got ctrl.Manager
	register := domains[d]
	domains[d] = func(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
		got = mgr
		return register(ctx, mgr, bus, o)
	}
	t.Cleanup(func() { domains[d] = register })
	r := start(t, e.runOptions(t, d))
	require.NotNil(t, got)

	c := got.GetClient()
	var list corev1.ConfigMapList
	require.NoError(t, got.GetAPIReader().List(ctx, &list, client.InNamespace("default")), "reads pass through")
	var cm corev1.ConfigMap
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(target), &cm), "reads pass through")

	fresh := func() *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "agent-wrote-this", Namespace: "default"}}
	}
	for verb, write := range map[string]func() error{
		"create":        func() error { return c.Create(ctx, fresh()) },
		"update":        func() error { return c.Update(ctx, cm.DeepCopy()) },
		"patch":         func() error { return c.Patch(ctx, cm.DeepCopy(), client.MergeFrom(&cm)) },
		"delete":        func() error { return c.Delete(ctx, cm.DeepCopy()) },
		"deleteAllOf":   func() error { return c.DeleteAllOf(ctx, &corev1.ConfigMap{}, client.InNamespace("default")) },
		"status update": func() error { return c.Status().Update(ctx, cm.DeepCopy()) },
		"subresource patch": func() error {
			return c.SubResource("status").Patch(ctx, cm.DeepCopy(), client.MergeFrom(&cm))
		},
	} {
		assert.ErrorIs(t, write(), k8s.ErrAgentWrite, verb)
	}
	assert.True(t, apierrors.IsNotFound(admin.Get(ctx, client.ObjectKeyFromObject(fresh()), &corev1.ConfigMap{})),
		"the refused create reached the apiserver")
	require.NoError(t, admin.Get(ctx, client.ObjectKeyFromObject(target), &corev1.ConfigMap{}), "the refused delete reached the apiserver")

	got.GetEventRecorderFor("agent-readonly").Event(&cm, corev1.EventTypeNormal, "AgentProbe", "a core/v1 Event")
	got.GetEventRecorder("agent-readonly").Eventf(&cm, nil, corev1.EventTypeNormal, "AgentProbe", "Probe", "an events.k8s.io Event")
	// A real recorder writes within its broadcaster's next flush; give it
	// longer than that before reading that nothing came.
	time.Sleep(3 * time.Second)
	var core corev1.EventList
	require.NoError(t, admin.List(ctx, &core, client.InNamespace("default")))
	var modern eventsv1.EventList
	require.NoError(t, admin.List(ctx, &modern, client.InNamespace("default")))
	for _, ev := range core.Items {
		assert.NotEqual(t, "AgentProbe", ev.Reason, "an agent recorded a core/v1 Event: %s", ev.Message)
	}
	for _, ev := range modern.Items {
		assert.NotEqual(t, "AgentProbe", ev.Reason, "an agent recorded an events.k8s.io Event: %s", ev.Note)
	}
	require.NoError(t, r.stop(t))
}
