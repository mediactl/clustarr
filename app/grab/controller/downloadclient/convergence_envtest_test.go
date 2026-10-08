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

package downloadclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	anatorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mediactl/clustarr/app/grab/engine"
	engtorrent "github.com/mediactl/clustarr/app/grab/engine/torrent"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/download"
	dltorrent "github.com/mediactl/clustarr/pkg/download/torrent"
	cevents "github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// recreatableKV is a bucket that can be "recreated" -- a NATS data loss --
// as JetStream shows one: a new creation time, and the open watch closed.
type recreatableKV struct {
	cevents.KV
	mu      sync.Mutex
	created time.Time
	closers []chan struct{}
}

func (k *recreatableKV) Status(ctx context.Context) (cevents.KVStatus, error) {
	st, err := k.KV.Status(ctx)
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.created.IsZero() {
		k.created = st.Created
	}
	st.Created = k.created
	return st, err
}

func (k *recreatableKV) Watch(ctx context.Context, key string, opts ...cevents.WatchOption) (<-chan cevents.Entry, error) {
	inner, err := k.KV.Watch(ctx, key, opts...)
	if err != nil {
		return nil, err
	}
	out := make(chan cevents.Entry)
	done := make(chan struct{})
	k.mu.Lock()
	k.closers = append(k.closers, done)
	k.mu.Unlock()
	go func() {
		defer close(out)
		for {
			select {
			case en, ok := <-inner:
				if !ok {
					return
				}
				select {
				case out <- en:
				case <-done:
					return
				case <-ctx.Done():
					return
				}
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (k *recreatableKV) recreate() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.created = k.created.Add(time.Hour)
	for _, d := range k.closers {
		close(d)
	}
	k.closers = nil
}

// recreatableBus is the membus bus with clustarr-transfers recreatable.
type recreatableBus struct {
	*membus.Bus
	transfers *recreatableKV
}

func (b *recreatableBus) KV(name string) cevents.KV {
	if name == cevents.BucketTransfers {
		return b.transfers
	}
	return b.Bus.KV(name)
}

// collect subscribes to ordinal's engine durable and records every command.
func collect(t *testing.T, e *dcEnv, client string, ordinal int32) func() []schema.EngineCommand {
	t.Helper()
	var mu sync.Mutex
	var got []schema.EngineCommand
	stop, err := e.bus.Subscribe(e.ctx, cevents.EngineConsumer(client, ordinal).Subscription(), func(_ context.Context, m cevents.Message) error {
		var c schema.EngineCommand
		if err := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &c); err != nil {
			return err
		}
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)
	return func() []schema.EngineCommand {
		mu.Lock()
		defer mu.Unlock()
		return append([]schema.EngineCommand(nil), got...)
	}
}

// TestARecreatedTransfersBucketStartsAResync: when clustarr-transfers is
// recreated (a NATS data loss), every DownloadClient takes a new resyncSeq
// and each engine is sent desired: resync at it, again until its record
// reports it (§6.7, "Resync") -- the point before which lifecycle judges no
// entry missing its transfer (TestAnEntryIsNotJudgedMissingBeforeResync).
func TestARecreatedTransfersBucketStartsAResync(t *testing.T) {
	e := newDCEnv(t)
	rb := &recreatableBus{Bus: e.bus, transfers: &recreatableKV{KV: e.bus.KV(cevents.BucketTransfers)}}
	e.r.Bus = rb
	dc := e.client(torrentClient("qbt", 2))
	e.record(dc, 0, nil)
	e.record(dc, 1, nil)
	e.reconcile("qbt")
	assert.Zero(t, e.get("qbt").Status.ResyncSeq, "no resync before a loss")
	got := []func() []schema.EngineCommand{collect(t, e, "qbt", 0), collect(t, e, "qbt", 1)}

	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer q.ShutDown()
	src := e.r.transfersRecreated(e.c)
	require.NoError(t, src.Start(e.ctx, q))
	time.Sleep(200 * time.Millisecond) // the source's first open
	rb.transfers.recreate()
	require.Eventually(t, func() bool { return q.Len() > 0 }, 15*time.Second, 20*time.Millisecond, "the recreation enqueues every client")
	req, _ := q.Get()
	assert.Equal(t, "qbt", req.Name)
	q.Done(req)

	res := e.reconcile("qbt")
	seq := e.get("qbt").Status.ResyncSeq
	require.Positive(t, seq, "a new resyncSeq")
	assert.GreaterOrEqual(t, seq, e.clock().UnixMilli(), "records.NextSeq: above the clock's millisecond")
	assert.Equal(t, resyncRepublish, res.RequeueAfter, "republished until every engine reports it")
	for o, g := range got {
		require.Eventually(t, func() bool { return len(g()) >= 1 }, 5*time.Second, 10*time.Millisecond, "ordinal %d", o)
		c := g()[0]
		assert.Equal(t, schema.EngineDesiredResync, c.Desired)
		assert.Equal(t, seq, c.ResyncSeq)
	}

	e.record(dc, 0, func(r *schema.EngineRecord) { r.ResyncSeq = seq })
	res = e.reconcile("qbt")
	assert.Equal(t, resyncRepublish, res.RequeueAfter, "ordinal 1 has not reported it yet")
	assert.Equal(t, seq, e.get("qbt").Status.ResyncSeq, "the same resync, not a new one")
	e.record(dc, 1, func(r *schema.EngineRecord) { r.ResyncSeq = seq })
	res = e.reconcile("qbt")
	assert.Equal(t, recheckInterval, res.RequeueAfter, "every engine reported the resync")
	assert.Equal(t, seq, e.get("qbt").Status.ResyncSeq)
}

// TestAnUnidentifiedTransferIsRemovedAfterTheGraceWithItsBytesKept is §6.7's
// "transfer with no claim at all" end to end: a real torrent engine holds a
// transfer no journal names (added by hand) and lists it unidentified; the
// DownloadClient controller waits lifecycle.UnidentifiedGrace from first
// seeing it, then commands its removal by download id with removeData
// false, once, with UnidentifiedTransferRemoved; the engine removes the
// transfer and its bytes stay on disk.
func TestAnUnidentifiedTransferIsRemovedAfterTheGraceWithItsBytesKept(t *testing.T) {
	e := newDCEnv(t)
	dc := e.client(torrentClient("qbt", 1))
	e.reconcile("qbt") // ensures the engine durable

	// A transfer added by hand: its bytes on disk, no descriptor, no journal.
	dataDir, stateDir := t.TempDir(), t.TempDir()
	content := make([]byte, 256<<10)
	_, _ = rand.New(rand.NewSource(11)).Read(content)
	file := filepath.Join(dataDir, "movie", "by-hand", "Some.Film.2001.mkv")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, content, 0o644))
	info := metainfo.Info{PieceLength: 32 << 10}
	require.NoError(t, info.BuildFromFilePath(file))
	infoBytes, err := bencode.Marshal(info)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, (&metainfo.MetaInfo{InfoBytes: infoBytes}).Write(&buf))

	cl, err := dltorrent.New(dltorrent.Config{DataDir: dataDir, ListenHost: anatorrent.LoopbackListenHost, NoDHT: true, DisableTrackers: true})
	require.NoError(t, err)
	defer func() { _ = cl.(io.Closer).Close() }()
	eng := &engtorrent.Engine{Client: cl, StateDir: stateDir}
	_, err = eng.ReAttach(e.ctx)
	require.NoError(t, err)
	id, err := cl.Add(e.ctx, download.AddRequest{Name: "by-hand", Category: "movie", Payload: buf.Bytes()})
	require.NoError(t, err)
	tr := &engtorrent.Transfers{Engine: eng}

	item := schema.ItemRef{Kind: "DownloadClient", Ref: schema.Ref{Namespace: dc.Namespace, Name: dc.Name, UID: string(dc.UID)}}
	rep := &engine.Reporter{
		Transfers: records.NewWriter(e.bus.KV(cevents.BucketTransfers), agentrecords.Transfers(), "qbt-engine-0", "test"),
		Engines:   records.NewWriter(e.bus.KV(cevents.BucketEngines), agentrecords.Engines(), "qbt-engine-0", "test"),
		Source:    tr, Client: item, Ordinal: 0, Engine: "qbt-0", Pod: "qbt-engine-0", BootID: "boot-1",
	}
	repCtx, stopRep := context.WithCancel(e.ctx)
	repDone := make(chan struct{})
	go func() {
		defer close(repDone)
		_ = rep.Start(repCtx)
	}()
	defer func() {
		stopRep()
		<-repDone
	}()
	require.Eventually(t, rep.Ready, 10*time.Second, 10*time.Millisecond)
	stop, err := e.bus.Subscribe(e.ctx, cevents.EngineConsumer("qbt", 0).Subscription(),
		(&engine.Handler{Transfers: tr, Reporter: rep, Engine: "qbt-0"}).Handle)
	require.NoError(t, err)
	defer stop()

	rec, _, ok, err := e.r.Engines.Get(e.ctx, agentrecords.EngineKey(string(dc.UID), 0))
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, rec.Unidentified, 1, "the engine lists the transfer no journal names")
	assert.Equal(t, id, rec.Unidentified[0].DownloadID)

	removals := func() int { return countOf(eventReasons(e.engineRec), ReasonUnidentifiedTransferRemoved) }
	e.reconcile("qbt") // first seen
	assert.Equal(t, int32(1), e.get("qbt").Status.UnidentifiedTransfers)
	e.advance(lifecycle.UnidentifiedGrace - time.Minute)
	e.reconcile("qbt")
	assert.Zero(t, removals(), "inside the grace")
	time.Sleep(300 * time.Millisecond)
	_, err = cl.Get(e.ctx, id)
	require.NoError(t, err, "still held inside the grace")

	e.advance(time.Minute + time.Second)
	e.reconcile("qbt")
	assert.Equal(t, 1, removals(), "UnidentifiedTransferRemoved")
	require.Eventually(t, func() bool {
		_, err := cl.Get(e.ctx, id)
		return errors.Is(err, download.ErrNotFound)
	}, 10*time.Second, 20*time.Millisecond, "the engine removed the transfer")
	got, err := os.ReadFile(file)
	require.NoError(t, err, "its bytes stay")
	assert.Equal(t, content, got)

	e.advance(time.Minute)
	e.reconcile("qbt")
	assert.Zero(t, removals(), "commanded once")
}
