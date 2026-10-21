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

package engine_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/grab/engine"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

const (
	testNS     = "media"
	clientUID  = "dc-uid-1"
	entryID    = "heat-1995-7f3a9c2e"
	entryUID   = "6a1b2c3d-0000-4000-8000-000000000001"
	engineName = "qbt-0"
)

func owner() schema.ItemRef {
	return schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: testNS, Name: "heat-1995", UID: "owner-uid-1"}}
}

func dcRef() schema.ItemRef {
	return schema.ItemRef{Kind: "DownloadClient", Ref: schema.Ref{Namespace: testNS, Name: "qbt", UID: clientUID}}
}

// command is the manager's present command for entry id/uid at seq.
func command(id, uid string, seq int64, desired string) schema.EngineCommand {
	return schema.EngineCommand{
		Seq: seq, Owner: owner(), Entry: schema.EntryRef{ID: id, UID: uid}, Desired: desired,
		Release: schema.CommandRelease{Title: "Heat.1995.1080p.BluRay.x264-GRP", GUID: "guid-1", IndexerRef: "tracker"},
		Claim: schema.TransferClaim{
			Owner: owner(), ProviderID: "tmdb:949", Entry: schema.EntryRef{ID: id, UID: uid},
			Indexer: "tracker", GUID: "guid-1", RemoveDataOnDelete: true,
		},
		IssuedAt: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC),
	}
}

// journaled is a transfer an earlier command added, at seq.
func journaled(id, uid string, seq int64) engine.Held {
	cmd := command(id, uid, seq, schema.EngineDesiredPresent)
	return engine.Held{
		Name:    id,
		Journal: engine.NextJournal(engine.Journal{}, cmd, time.Now()),
		Item:    download.Item{ID: "dl-" + uid, Name: id, Status: download.StatusDownloading, Stage: "transferring", AddedAt: time.Now().Add(-time.Hour)},
		Found:   true,
	}
}

// memTransfers is an engine's client and journal in memory: Apply adds,
// updates or removes as the real engines do, and every call is logged.
type memTransfers struct {
	mu    sync.Mutex
	held  []engine.Held
	calls []string
	// applyErr, when set, fails the next Apply (and is cleared).
	applyErr error
	// removed records each RemoveByID's removeData.
	removed map[string]bool
}

func (m *memTransfers) log(format string, args ...any) {
	m.calls = append(m.calls, fmt.Sprintf(format, args...))
}

func (m *memTransfers) Calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

func (m *memTransfers) add(h engine.Held) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.held = append(m.held, h)
}

func (m *memTransfers) get(name string) (engine.Held, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range m.held {
		if h.Name == name {
			return h, true
		}
	}
	return engine.Held{}, false
}

func (m *memTransfers) List(context.Context) ([]engine.Held, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log("list")
	return append([]engine.Held(nil), m.held...), nil
}

func (m *memTransfers) Apply(_ context.Context, cmd schema.EngineCommand, cur *engine.Held) (engine.Held, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	curName := ""
	if cur != nil {
		curName = cur.Name
	}
	m.log("apply %s seq=%d cur=%s", cmd.Desired, cmd.Seq, curName)
	if err := m.applyErr; err != nil {
		m.applyErr = nil
		return engine.Held{}, err
	}
	idx := -1
	for i := range m.held {
		if cur != nil && m.held[i].Name == cur.Name {
			idx = i
		}
	}
	if cmd.Desired == schema.EngineDesiredAbsent {
		var h engine.Held
		if idx >= 0 {
			h = m.held[idx]
			m.held = append(m.held[:idx], m.held[idx+1:]...)
		}
		h.Journal = engine.NextJournal(h.Journal, cmd, time.Now())
		h.Found = false
		return h, nil
	}
	h := engine.Held{
		Name:  cmd.Entry.ID,
		Item:  download.Item{ID: "dl-" + cmd.Entry.UID, Name: cmd.Entry.ID, Status: download.StatusQueued, AddedAt: time.Now()},
		Found: true,
	}
	if idx >= 0 {
		h = m.held[idx]
	}
	h.Journal = engine.NextJournal(h.Journal, cmd, time.Now())
	if idx >= 0 {
		m.held[idx] = h
	} else {
		m.held = append(m.held, h)
	}
	return h, nil
}

func (m *memTransfers) RemoveByID(_ context.Context, id string, removeData bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log("removeByID %s data=%t", id, removeData)
	if m.removed == nil {
		m.removed = map[string]bool{}
	}
	for i := range m.held {
		if m.held[i].Item.ID == id {
			m.held = append(m.held[:i], m.held[i+1:]...)
			m.removed[id] = removeData
			return nil
		}
	}
	return download.ErrNotFound
}

// RemoveDetached records a removal by entry for a transfer the client did
// not list (engine.DetachedRemover).
func (m *memTransfers) RemoveDetached(_ context.Context, entryUID string, removeData bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log("removeDetached %s data=%t", entryUID, removeData)
	return nil
}

// gatedKV refuses every write while closed: a transfers bucket the engine
// cannot write to (a full stream, a lost connection).
type gatedKV struct {
	events.KV
	mu     sync.Mutex
	closed bool
	log    *[]string
	name   string
}

var errGated = errors.New("gatedKV: writes refused")

func (g *gatedKV) setClosed(c bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = c
}

func (g *gatedKV) refuse(key string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return errGated
	}
	if g.log != nil {
		*g.log = append(*g.log, g.name+" "+key)
	}
	return nil
}

func (g *gatedKV) Create(ctx context.Context, key string, v []byte, o ...events.KVOption) (uint64, error) {
	if err := g.refuse(key); err != nil {
		return 0, err
	}
	return g.KV.Create(ctx, key, v, o...)
}

func (g *gatedKV) Update(ctx context.Context, key string, v []byte, rev uint64) (uint64, error) {
	if err := g.refuse(key); err != nil {
		return 0, err
	}
	return g.KV.Update(ctx, key, v, rev)
}

func (g *gatedKV) Put(ctx context.Context, key string, v []byte) (uint64, error) {
	if err := g.refuse(key); err != nil {
		return 0, err
	}
	return g.KV.Put(ctx, key, v)
}

// harness is one engine instance over a membus clustarr-transfers and
// clustarr-engines.
type harness struct {
	bus       *membus.Bus
	clock     *clockwork.FakeClock
	src       *memTransfers
	transfers *gatedKV
	engines   *gatedKV
	writes    *[]string
	rep       *engine.Reporter
	h         *engine.Handler
}

func newHarness(t *testing.T, held ...engine.Held) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clock := clockwork.NewFakeClockAt(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	bus := membus.New(clock)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	writes := &[]string{}
	hs := &harness{
		bus: bus, clock: clock, src: &memTransfers{held: held}, writes: writes,
		transfers: &gatedKV{KV: bus.KV(events.BucketTransfers), log: writes, name: "transfers"},
		engines:   &gatedKV{KV: bus.KV(events.BucketEngines), log: writes, name: "engines"},
	}
	hs.rep = &engine.Reporter{
		Transfers: records.NewWriter(hs.transfers, agentrecords.Transfers(), "qbt-0-pod", "test"),
		Engines:   records.NewWriter(hs.engines, agentrecords.Engines(), "qbt-0-pod", "test"),
		Source:    hs.src,
		Client:    dcRef(),
		Ordinal:   0,
		Engine:    engineName,
		Pod:       "qbt-0-pod",
		BootID:    "boot-1",
	}
	hs.h = &engine.Handler{Transfers: hs.src, Reporter: hs.rep, Engine: engineName}
	return hs
}

// start runs the reporter's re-attach report and waits for Ready.
func (hs *harness) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = hs.rep.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	require.Eventually(t, hs.rep.Ready, 5*time.Second, 5*time.Millisecond, "the re-attach report")
}

func (hs *harness) transfer(t *testing.T, uid string) (*schema.TransferRecord, bool) {
	t.Helper()
	rec, _, ok, err := records.NewReader(hs.bus.KV(events.BucketTransfers), agentrecords.Transfers()).
		Get(context.Background(), agentrecords.TransferKey(uid))
	require.NoError(t, err)
	return rec, ok
}

func (hs *harness) engineRecord(t *testing.T) *schema.EngineRecord {
	t.Helper()
	rec, _, ok, err := records.NewReader(hs.bus.KV(events.BucketEngines), agentrecords.Engines()).
		Get(context.Background(), agentrecords.EngineKey(clientUID, 0))
	require.NoError(t, err)
	require.True(t, ok, "the engine record")
	return rec
}

// msg is one delivery of cmd, for calling Handle directly.
type msg struct {
	env     *events.Envelope
	attempt uint64
}

func delivery(t *testing.T, cmd schema.EngineCommand) *msg {
	t.Helper()
	name, data, err := schema.Encode(cmd)
	require.NoError(t, err)
	return &msg{env: &events.Envelope{ID: events.MsgIDForEngineCommand(cmd.Entry.UID, cmd.Seq), Schema: name, Data: data}, attempt: 1}
}

func (m *msg) Ack(context.Context) error                { return nil }
func (m *msg) Nak(context.Context, time.Duration) error { return nil }
func (m *msg) Term(context.Context, string) error       { return nil }
func (m *msg) InProgress(context.Context) error         { return nil }
func (m *msg) Envelope() *events.Envelope               { return m.env }
func (m *msg) Subject() string                          { return "clustarr.work.engine.qbt.0.x" }
func (m *msg) Attempt() uint64                          { return m.attempt }
