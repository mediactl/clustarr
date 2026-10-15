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

package torrent

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	anatorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/engine"
	"github.com/mediactl/clustarr/pkg/download"
	dltorrent "github.com/mediactl/clustarr/pkg/download/torrent"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

const (
	cmdNS       = "media"
	cmdEntryID  = "heat-1995-7f3a9c2e"
	cmdEntryUID = "6a1b2c3d-0000-4000-8000-000000000001"
)

func cmdOwner() schema.ItemRef {
	return schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: cmdNS, Name: "heat-1995", UID: "owner-uid-1"}}
}

func presentCmd(seq int64, src commonv1alpha1.DownloadSource) schema.EngineCommand {
	entry := schema.EntryRef{ID: cmdEntryID, UID: cmdEntryUID}
	return schema.EngineCommand{
		Seq: seq, Owner: cmdOwner(), Entry: entry, Desired: schema.EngineDesiredPresent,
		Protocol: commonv1alpha1.ProtocolTorrent, Source: &src, Category: "movie",
		Release:  schema.CommandRelease{Title: "Heat.1995.1080p.BluRay.x264-GRP", GUID: "guid-1", IndexerRef: "tracker"},
		Claim:    schema.TransferClaim{Owner: cmdOwner(), ProviderID: "tmdb:949", Entry: entry, Indexer: "tracker", GUID: "guid-1", RemoveDataOnDelete: true},
		IssuedAt: time.Now().UTC(),
	}
}

// reporting is one engine's Reporter over a membus clustarr-transfers and
// clustarr-engines, started (the re-attach report), and its Handler.
type reporting struct {
	bus *membus.Bus
	rep *engine.Reporter
	h   *engine.Handler
}

func newReporting(t *testing.T, tr engine.Transfers, bootID string) *reporting {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	rep := &engine.Reporter{
		Transfers: records.NewWriter(bus.KV(events.BucketTransfers), agentrecords.Transfers(), "qbt-0", "test"),
		Engines:   records.NewWriter(bus.KV(events.BucketEngines), agentrecords.Engines(), "qbt-0", "test"),
		Source:    tr,
		Client:    schema.ItemRef{Kind: "DownloadClient", Ref: schema.Ref{Namespace: cmdNS, Name: "qbt", UID: "dc-uid-1"}},
		Engine:    "qbt-0", Pod: "qbt-0", BootID: bootID,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = rep.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = bus.Close()
	})
	require.Eventually(t, rep.Ready, 10*time.Second, 5*time.Millisecond)
	return &reporting{bus: bus, rep: rep, h: &engine.Handler{Transfers: tr, Reporter: rep, Engine: "qbt-0"}}
}

func (r *reporting) record(t *testing.T) (*schema.TransferRecord, bool) {
	t.Helper()
	rec, _, ok, err := records.NewReader(r.bus.KV(events.BucketTransfers), agentrecords.Transfers()).
		Get(context.Background(), agentrecords.TransferKey(cmdEntryUID))
	require.NoError(t, err)
	return rec, ok
}

// cmdMsg is one delivery of an engine command, for Handle.
type cmdMsg struct{ env *events.Envelope }

func cmdDelivery(t *testing.T, cmd schema.EngineCommand) *cmdMsg {
	t.Helper()
	name, data, err := schema.Encode(cmd)
	require.NoError(t, err)
	return &cmdMsg{env: &events.Envelope{ID: events.MsgIDForEngineCommand(cmd.Entry.UID, cmd.Seq), Schema: name, Data: data}}
}

func (m *cmdMsg) Ack(context.Context) error                { return nil }
func (m *cmdMsg) Nak(context.Context, time.Duration) error { return nil }
func (m *cmdMsg) Term(context.Context, string) error       { return nil }
func (m *cmdMsg) InProgress(context.Context) error         { return nil }
func (m *cmdMsg) Envelope() *events.Envelope               { return m.env }
func (m *cmdMsg) Subject() string                          { return "clustarr.work.engine.qbt.0.x" }
func (m *cmdMsg) Attempt() uint64                          { return 1 }

// TestPayloadUnavailableIsReportedNotRetriedForever: a re-add whose payload
// the indexer no longer serves (an index answer marked gone, a 404 or 410
// link) is answered in the transfer record -- failed, payloadUnavailable --
// and acked, so the manager decides (a block before the import, SeedingLost
// after); a transient fetch failure, the indexer's own included, is retried
// instead.
func TestPayloadUnavailableIsReportedNotRetriedForever(t *testing.T) {
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/404.torrent":
			http.NotFound(w, r)
		case "/410.torrent":
			w.WriteHeader(http.StatusGone)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer gone.Close()
	url := func(p string) *string { s := gone.URL + p; return &s }

	for name, tc := range map[string]struct {
		src         commonv1alpha1.DownloadSource
		resolver    *FakeIndexerResolver
		unavailable bool
	}{
		"the indexer answers that the release is gone": {
			src:         commonv1alpha1.DownloadSource{IndexerDownload: &commonv1alpha1.IndexerDownload{IndexerRef: "tracker", GUID: "guid-1"}},
			resolver:    &FakeIndexerResolver{Response: schema.DownloadResponse{Error: "the indexer has no such release", Gone: true}},
			unavailable: true,
		},
		"the indexer is down": {
			src:      commonv1alpha1.DownloadSource{IndexerDownload: &commonv1alpha1.IndexerDownload{IndexerRef: "tracker", GUID: "guid-1"}},
			resolver: &FakeIndexerResolver{Response: schema.DownloadResponse{Error: "index: indexer returned HTTP 522"}},
		},
		"a 404 torrent link": {src: commonv1alpha1.DownloadSource{TorrentURL: url("/404.torrent")}, unavailable: true},
		"a 410 torrent link": {src: commonv1alpha1.DownloadSource{TorrentURL: url("/410.torrent")}, unavailable: true},
		"a 503 is transient": {src: commonv1alpha1.DownloadSource{TorrentURL: url("/503.torrent")}},
	} {
		t.Run(name, func(t *testing.T) {
			fc := newFakeClient()
			e := &Engine{Client: fc, StateDir: t.TempDir()}
			_, err := e.ReAttach(context.Background())
			require.NoError(t, err)
			tr := &Transfers{Engine: e, HTTPClient: gone.Client(), Resolver: tc.resolver}
			r := newReporting(t, tr, "boot-2")

			err = r.h.Handle(context.Background(), cmdDelivery(t, presentCmd(77, tc.src)))
			assert.Empty(t, fc.addRequests, "nothing was added")
			rec, ok := r.record(t)
			if !tc.unavailable {
				var retry *events.RetryError
				require.ErrorAs(t, err, &retry, "a transient failure is retried")
				assert.False(t, ok && rec.EngineFailureReason.IsFailure(), "and reported as nothing")
				return
			}
			require.NoError(t, err, "acked: never redelivered for the same seq")
			require.True(t, ok)
			assert.Equal(t, schema.TransferStateFailed, rec.State)
			assert.Equal(t, commonv1alpha1.DownloadFailurePayloadUnavailable, rec.EngineFailureReason)
			assert.Equal(t, int64(77), rec.Seq, "at the command's seq, so the manager reads it as the answer")
			assert.True(t, rec.Claimed)
			if tc.resolver != nil {
				assert.Len(t, tc.resolver.Requests, 1, "the indexer was asked once")
			}
		})
	}
}

// TestTheEngineRemovesNothingWithoutAnAbsentCommand: the engine decides no
// removal (§3.5 P111-P123, §6.7 "Data safety" 1): a transfer whose seed goal
// is met, whose import is recorded and the client may remove, or which
// failed, stays -- files and descriptor -- through every report and every
// present command; only an absent command above the journal's seq removes,
// with the data it names.
func TestTheEngineRemovesNothingWithoutAnAbsentCommand(t *testing.T) {
	ctx := context.Background()
	fc := newFakeClient()
	e := &Engine{Client: fc, StateDir: t.TempDir()}
	_, err := e.ReAttach(ctx)
	require.NoError(t, err)
	tr := &Transfers{Engine: e}
	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	src := commonv1alpha1.DownloadSource{MagnetURL: &magnet}

	held, err := tr.Apply(ctx, presentCmd(1, src), nil)
	require.NoError(t, err)
	id := held.Item.ID

	// Every state that once made an engine remove a transfer on its own.
	fc.mu.Lock()
	it := fc.items[id]
	it.Status = download.StatusCompleted
	it.Stage = "seeding"
	it.SeedGoalMet = true
	it.CanBeRemoved = true
	fc.items[id] = it
	fc.mu.Unlock()

	r := newReporting(t, tr, "boot-1")
	imported := presentCmd(2, src)
	imported.Imported = true
	imported.RemoveOnImport = true
	require.NoError(t, r.h.Handle(ctx, cmdDelivery(t, imported)))

	fc.mu.Lock()
	it = fc.items[id]
	it.Status = download.StatusFailed
	it.FailureReason = commonv1alpha1.DownloadFailureStalled
	fc.items[id] = it
	fc.mu.Unlock()
	require.NoError(t, r.rep.Resync(ctx, 1), "a report of the failure")
	require.NoError(t, r.h.Handle(ctx, cmdDelivery(t, presentCmd(3, src))))
	require.NoError(t, r.h.Handle(ctx, cmdDelivery(t, func() schema.EngineCommand {
		c := presentCmd(2, src)
		c.Desired = schema.EngineDesiredAbsent // stale: at or below the journal's seq
		return c
	}())))

	assert.Empty(t, fc.removeCalls, "nothing removed without an absent command above the journal's seq")
	_, err = fc.Get(ctx, id)
	require.NoError(t, err, "the transfer is still held")
	ds, errs := loadDescriptors(e.StateDir)
	require.Empty(t, errs)
	require.Len(t, ds, 1, "and its descriptor kept")
	rec, ok := r.record(t)
	require.True(t, ok)
	assert.Equal(t, schema.TransferStateFailed, rec.State, "the failure is reported, the decision left to the manager")

	absent := presentCmd(4, src)
	absent.Desired = schema.EngineDesiredAbsent
	absent.RemoveData = true
	require.NoError(t, r.h.Handle(ctx, cmdDelivery(t, absent)))
	require.Len(t, fc.removeCalls, 1)
	assert.Equal(t, removeCall{id: id, deleteData: true}, fc.removeCalls[0])
	rec, _ = r.record(t)
	assert.Equal(t, schema.TransferStateRemoved, rec.State)
	assert.Equal(t, int64(4), rec.Seq)
	assert.True(t, rec.DataRemoved)
}

// TestEngineRestartKeepsSeedingWithoutARehash: a real anacrolix engine
// seeding an imported transfer is stopped and started over the same state
// and data (§6.7, "Re-attach and the journal"; R13): it re-attaches the
// transfer from its descriptor, still imported and still seeding, reports it
// at the new boot -- and does not rehash: a byte changed on disk while it was
// down goes unnoticed, because the piece completion it kept says the data is
// whole.
func TestEngineRestartKeepsSeedingWithoutARehash(t *testing.T) {
	if testing.Short() {
		t.Skip("starts two real BitTorrent clients")
	}
	ctx := context.Background()
	dataDir, stateDir := t.TempDir(), t.TempDir()
	cfg := dltorrent.Config{
		DataDir: dataDir, ListenHost: anatorrent.LoopbackListenHost, NoDHT: true, DisableTrackers: true, Seed: true,
	}

	// The payload is already in place, where the client keeps this entry's
	// files (<DataDir>/<category>/<entry id>/), as after a finished download.
	content := make([]byte, 512<<10)
	_, _ = rand.New(rand.NewSource(7)).Read(content)
	file := filepath.Join(dataDir, "movie", cmdEntryID, "Heat.1995.mkv")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, content, 0o644))
	info := metainfo.Info{PieceLength: 32 << 10}
	require.NoError(t, info.BuildFromFilePath(file))
	infoBytes, err := bencode.Marshal(info)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, (&metainfo.MetaInfo{InfoBytes: infoBytes}).Write(&buf))
	resolver := &FakeIndexerResolver{Response: schema.DownloadResponse{Bytes: buf.Bytes()}}
	src := commonv1alpha1.DownloadSource{IndexerDownload: &commonv1alpha1.IndexerDownload{IndexerRef: "tracker", GUID: "guid-1"}}

	start := func() (*Engine, *Transfers, io.Closer) {
		c, err := dltorrent.New(cfg)
		require.NoError(t, err)
		e := &Engine{Client: c, StateDir: stateDir}
		_, err = e.ReAttach(ctx)
		require.NoError(t, err)
		return e, &Transfers{Engine: e, Resolver: resolver}, c.(io.Closer)
	}
	complete := func(tr *Transfers) engine.Held {
		var got engine.Held
		require.Eventually(t, func() bool {
			held, err := tr.List(ctx)
			if err != nil || len(held) != 1 {
				return false
			}
			got = held[0]
			return got.Item.Status == download.StatusCompleted && got.Item.RemainingBytes == 0
		}, 30*time.Second, 20*time.Millisecond)
		return got
	}

	// A goal the test never reaches, so the transfer seeds throughout (with
	// none, a goal is met at once and the client stops uploading).
	week := metav1.Duration{Duration: 7 * 24 * time.Hour}
	seedCmd := func(seq int64) schema.EngineCommand {
		c := presentCmd(seq, src)
		c.SeedCriteria = &commonv1alpha1.SeedCriteria{SeedTime: &week}
		return c
	}
	_, tr1, c1 := start()
	held, err := tr1.Apply(ctx, seedCmd(1), nil)
	require.NoError(t, err)
	held = complete(tr1)
	require.Equal(t, commonv1alpha1.DownloadStage("seeding"), held.Item.Stage, "seeding towards its goal")
	imp := seedCmd(2)
	imp.Imported = true
	_, err = tr1.Apply(ctx, imp, &held)
	require.NoError(t, err)
	require.NoError(t, c1.Close())

	// While the engine is down, a byte of the payload changes.
	damaged := bytes.Clone(content)
	damaged[300<<10] ^= 0xff
	require.NoError(t, os.WriteFile(file, damaged, 0o644))

	e2, tr2, c2 := start()
	defer func() { _ = c2.Close() }()
	require.True(t, e2.Ready())
	after := complete(tr2)
	assert.False(t, after.Item.SeedGoalMet, "the goal is a week of seeding away")
	assert.Equal(t, cmdEntryUID, after.Journal.EntryUID, "the journal came back with the transfer")
	assert.Equal(t, int64(2), after.Journal.Seq)
	assert.True(t, after.Journal.Imported, "still imported")
	assert.Equal(t, commonv1alpha1.DownloadStage("seeding"), after.Item.Stage, "still seeding")
	assert.Equal(t, int64(0), after.Item.RemainingBytes, "no rehash: the changed piece was not re-verified")

	r := newReporting(t, tr2, "boot-after-restart")
	rec, ok := r.record(t)
	require.True(t, ok, "reported at the new boot before ready")
	assert.Equal(t, "boot-after-restart", rec.BootID)
	assert.Equal(t, schema.TransferStatePresent, rec.State)
	assert.Equal(t, commonv1alpha1.DownloadStage("seeding"), rec.Stage)
	assert.Equal(t, int64(2), rec.Seq)
	require.NotNil(t, rec.Claim)
	assert.True(t, rec.Claim.Imported)
}
