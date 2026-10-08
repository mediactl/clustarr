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

package remediationtest

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/import/worker/probe/probetest"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/probe"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/probestore"
)

// SentTask is one probe task the loop published.
type SentTask struct {
	Subject, MsgID string
	Duplicate      bool
}

// RecordingBus is a membus that records every probe task published through
// it.
type RecordingBus struct {
	*membus.Bus
	mu   sync.Mutex
	sent []SentTask
}

// Publish implements events.Bus, recording a probe task.
func (b *RecordingBus) Publish(ctx context.Context, subject string, e *events.Envelope, opts ...events.PublishOption) (events.Receipt, error) {
	rcpt, err := b.Bus.Publish(ctx, subject, e, opts...)
	if err == nil && strings.HasPrefix(subject, "clustarr.work.probe.") {
		b.mu.Lock()
		b.sent = append(b.sent, SentTask{Subject: subject, MsgID: events.ResolvePublishOptions(e, opts).MsgID, Duplicate: rcpt.Duplicate})
		b.mu.Unlock()
	}
	return rcpt, err
}

// ProbeHarness is the probe queue a loop test talks to (F3.2's W4.13
// harness, shared by every loop package whose tests run the probe planner):
// a membus with the shipped topology, the probe planner's store on it (every
// task the loop publishes recorded), and, when a test asks for it, the
// import domain's real probe worker answering through a fake Prober.
type ProbeHarness struct {
	Bus     *membus.Bus
	Pub     *RecordingBus
	Store   *probestore.Store
	Agent   *probetest.Agent
	Adapter *probe.Adapter
}

// NewProbeHarness is a harness with no agent: a test answers by hand.
func NewProbeHarness(t testing.TB) *ProbeHarness {
	t.Helper()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })
	pub := &RecordingBus{Bus: bus}
	store := probestore.New(pub)
	return &ProbeHarness{Bus: bus, Pub: pub, Store: store, Adapter: probe.New(probe.Options{Bus: pub, Probes: store})}
}

// WithAgent starts the import domain's probe worker on h, answering through p.
func (h *ProbeHarness) WithAgent(t testing.TB, p mediainfo.Prober) *ProbeHarness {
	t.Helper()
	h.Agent = probetest.Start(t, h.Bus, p)
	return h
}

// SetVersion makes the probe planner want probe version v (0 is
// mediainfo.ProbeVersion). A reconciler built before keeps its adapter.
func (h *ProbeHarness) SetVersion(v int32) {
	h.Adapter = probe.New(probe.Options{Bus: h.Pub, Probes: h.Store, Version: v})
}

// Probe is the probe planner, bound.
func (h *ProbeHarness) Probe() remediation.Bound {
	return remediation.Bind[mediafile.ProbeInput](h.Adapter)
}

// Loop is e's loop over the probe planner and planners, its bus h's: the
// loop's record writes and task publishes reach the harness's store and
// agent.
func (h *ProbeHarness) Loop(t testing.TB, e *Env, planners ...remediation.Bound) *remediation.Reconciler {
	t.Helper()
	e.Loop.Bus = h.Pub
	return e.Reconciler(t, append([]remediation.Bound{h.Probe()}, planners...)...)
}

// Sent is every probe task the loop published, in order.
func (h *ProbeHarness) Sent() []SentTask {
	h.Pub.mu.Lock()
	defer h.Pub.mu.Unlock()
	return append([]SentTask(nil), h.Pub.sent...)
}

// Record reads uid's probe record.
func (h *ProbeHarness) Record(t testing.TB, uid string) probestore.Current {
	t.Helper()
	cur, err := h.Store.Get(context.Background(), uid)
	require.NoError(t, err)
	return cur
}

// PutRecord writes rec as uid's record, whatever is there.
func (h *ProbeHarness) PutRecord(t testing.TB, uid string, rec schema.ProbeRecord) {
	t.Helper()
	b, err := json.Marshal(rec)
	require.NoError(t, err)
	_, err = h.Bus.KV(events.BucketProbes).Put(context.Background(), events.ProbeKey(uid), b)
	require.NoError(t, err)
}

// AnswerWith answers uid's outstanding request with ans, whose path, hash,
// version and time default to the request's and now.
func (h *ProbeHarness) AnswerWith(t testing.TB, uid string, ans schema.ProbeRecord) {
	t.Helper()
	cur := h.Record(t, uid)
	require.True(t, cur.OK && cur.Record.State == schema.ProbeRequested, "no request outstanding for %s: %+v", uid, cur.Record)
	if ans.Path == "" {
		ans.Path = cur.Record.Path
	}
	if ans.ProbeHash == "" {
		ans.ProbeHash = cur.Record.ProbeHash
	}
	if ans.ProbeVersion == 0 {
		ans.ProbeVersion = mediainfo.ProbeVersion
	}
	if ans.ProbedAt.IsZero() {
		ans.ProbedAt = time.Now().UTC()
	}
	ok, err := h.Store.Answer(context.Background(), probestore.TaskOf(cur.Record), ans)
	require.NoError(t, err)
	require.True(t, ok, "the answer was dropped")
}

// Answer answers uid's outstanding request as probed with mi.
func (h *ProbeHarness) Answer(t testing.TB, uid string, mi *commonv1.MediaInfo) {
	t.Helper()
	h.AnswerWith(t, uid, schema.ProbeRecord{RecordHeader: schema.RecordHeader{State: schema.ProbeProbed}, MediaInfo: mi})
}

// ReconcileOnce runs one pass of nn's file key.
func ReconcileOnce(t testing.TB, r *remediation.Reconciler, nn types.NamespacedName) reconcile.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), remediation.Key{Kind: remediation.KindMediaFile, Namespace: nn.Namespace, Name: nn.Name})
	require.NoError(t, err)
	return res
}

// Settle runs a pass, lets the agent answer the probe it asked for, and runs
// another: what a pass, the import domain and S16's wake do on a cluster.
func Settle(t testing.TB, h *ProbeHarness, r *remediation.Reconciler, c client.Reader, nn types.NamespacedName) reconcile.Result {
	t.Helper()
	ReconcileOnce(t, r, nn)
	return Answered(t, h, r, c, nn)
}

// Answered waits for the agent to answer nn's outstanding request, if there
// is one, and runs a pass.
func Answered(t testing.TB, h *ProbeHarness, r *remediation.Reconciler, c client.Reader, nn types.NamespacedName) reconcile.Result {
	t.Helper()
	require.NotNil(t, h.Agent, "Settle needs WithAgent")
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(context.Background(), nn, &mf))
	if cur := h.Record(t, string(mf.UID)); cur.OK && cur.Record.State == schema.ProbeRequested {
		h.Agent.Wait(t, string(mf.UID), cur.Record.Seq)
	}
	return ReconcileOnce(t, r, nn)
}

// FakeProber is FakeProbe as the import domain's Prober.
var FakeProber = probetest.ProberFunc(FakeProbe)

// FakeMediaInfo is what FakeProbe answers.
func FakeMediaInfo() *commonv1.MediaInfo {
	mi, _, _ := FakeProbe(context.Background(), os.DevNull)
	return mi
}

// FailKVOnce wraps bus so the first Create or Update in bucket fails with
// events.ErrClosed, as a lost connection would; everything else passes
// through.
func FailKVOnce(bus events.Bus, bucket string) events.Bus {
	return &failKVBus{Bus: bus, bucket: bucket}
}

type failKVBus struct {
	events.Bus
	bucket string
	failed atomic.Bool
}

func (b *failKVBus) KV(bucket string) events.KV {
	kv := b.Bus.KV(bucket)
	if bucket != b.bucket {
		return kv
	}
	return &failKV{KV: kv, failed: &b.failed}
}

type failKV struct {
	events.KV
	failed *atomic.Bool
}

func (k *failKV) Create(ctx context.Context, key string, val []byte, opts ...events.KVOption) (uint64, error) {
	if k.failed.CompareAndSwap(false, true) {
		return 0, events.ErrClosed
	}
	return k.KV.Create(ctx, key, val, opts...)
}

func (k *failKV) Update(ctx context.Context, key string, val []byte, rev uint64) (uint64, error) {
	if k.failed.CompareAndSwap(false, true) {
		return 0, events.ErrClosed
	}
	return k.KV.Update(ctx, key, val, rev)
}
