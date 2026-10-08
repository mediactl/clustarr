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

package usenet_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/engine"
	usenetengine "github.com/mediactl/clustarr/app/grab/engine/usenet"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// journaledClient is fakeDownloadClient keeping each job's journal, as the
// real client's scratch manifest does (download.Journaled).
type journaledClient struct {
	*fakeDownloadClient
	jmu      sync.Mutex
	journals map[string][]byte
}

func (j *journaledClient) SetJournal(_ context.Context, id string, b []byte) error {
	j.jmu.Lock()
	defer j.jmu.Unlock()
	j.journals[id] = append([]byte(nil), b...)
	return nil
}

func (j *journaledClient) Journal(_ context.Context, id string) ([]byte, error) {
	j.jmu.Lock()
	defer j.jmu.Unlock()
	b, ok := j.journals[id]
	if !ok {
		return nil, download.ErrNotFound
	}
	return b, nil
}

func (j *journaledClient) FindByName(_ context.Context, name string) (download.Item, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, it := range j.items {
		if it.Name == name {
			return it, nil
		}
	}
	return download.Item{}, download.ErrNotFound
}

type usenetMsg struct{ env *events.Envelope }

func (m *usenetMsg) Ack(context.Context) error                { return nil }
func (m *usenetMsg) Nak(context.Context, time.Duration) error { return nil }
func (m *usenetMsg) Term(context.Context, string) error       { return nil }
func (m *usenetMsg) InProgress(context.Context) error         { return nil }
func (m *usenetMsg) Envelope() *events.Envelope               { return m.env }
func (m *usenetMsg) Subject() string                          { return "clustarr.work.engine.sab.0.x" }
func (m *usenetMsg) Attempt() uint64                          { return 1 }

// TestTheEngineRemovesNothingWithoutAnAbsentCommand is the usenet half of
// A3.6's rule (the torrent half is app/grab/engine/torrent's): a job whose
// import is recorded with removeOnImport, or which failed, is not removed
// by the engine; only an absent command above the journal's seq removes it.
func TestTheEngineRemovesNothingWithoutAnAbsentCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const entryID, entryUID = "heat-1995-7f3a9c2e", "6a1b2c3d-0000-4000-8000-000000000001"
	owner := schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: "media", Name: "heat-1995", UID: "owner-uid-1"}}
	cmd := func(seq int64, desired string) schema.EngineCommand {
		e := schema.EntryRef{ID: entryID, UID: entryUID}
		return schema.EngineCommand{
			Seq: seq, Owner: owner, Entry: e, Desired: desired, Protocol: commonv1alpha1.ProtocolUsenet,
			Release: schema.CommandRelease{Title: "Heat.1995.1080p", GUID: "nzb-guid"},
			Claim:   schema.TransferClaim{Owner: owner, Entry: e, GUID: "nzb-guid", RemoveDataOnDelete: true},
		}
	}

	jc := &journaledClient{fakeDownloadClient: newFakeDownloadClient(), journals: map[string][]byte{}}
	jc.setItem(download.Item{
		ID: "job-1", Name: entryID, Status: download.StatusCompleted, Stage: "done",
		CanBeRemoved: true, AddedAt: time.Now().Add(-time.Hour),
	})
	j, err := engine.MarshalJournal(engine.NextJournal(engine.Journal{}, cmd(1, schema.EngineDesiredPresent), time.Now()))
	require.NoError(t, err)
	require.NoError(t, jc.SetJournal(ctx, "job-1", j))
	tr := &usenetengine.Transfers{Client: jc}

	bus := membus.New(clockwork.NewRealClock())
	defer func() { _ = bus.Close() }()
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	rep := &engine.Reporter{
		Transfers: records.NewWriter(bus.KV(events.BucketTransfers), agentrecords.Transfers(), "sab-0", "test"),
		Engines:   records.NewWriter(bus.KV(events.BucketEngines), agentrecords.Engines(), "sab-0", "test"),
		Source:    tr,
		Client:    schema.ItemRef{Kind: "DownloadClient", Ref: schema.Ref{Namespace: "media", Name: "sab", UID: "dc-uid-2"}},
		Engine:    "sab-0", Pod: "sab-0", BootID: "boot-1",
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = rep.Start(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()
	require.Eventually(t, rep.Ready, 5*time.Second, 5*time.Millisecond)
	h := &engine.Handler{Transfers: tr, Reporter: rep, Engine: "sab-0"}
	handle := func(c schema.EngineCommand) {
		t.Helper()
		name, data, err := schema.Encode(c)
		require.NoError(t, err)
		require.NoError(t, h.Handle(ctx, &usenetMsg{env: &events.Envelope{Schema: name, Data: data}}))
	}
	record := func() *schema.TransferRecord {
		t.Helper()
		rec, _, ok, err := records.NewReader(bus.KV(events.BucketTransfers), agentrecords.Transfers()).
			Get(ctx, agentrecords.TransferKey(entryUID))
		require.NoError(t, err)
		require.True(t, ok)
		return rec
	}

	imported := cmd(2, schema.EngineDesiredPresent)
	imported.Imported, imported.RemoveOnImport = true, true
	handle(imported)
	assert.Equal(t, []string{"job-1"}, jc.markImportedIDs, "the import is recorded")

	jc.mu.Lock()
	it := jc.items["job-1"]
	it.Status, it.FailureReason = download.StatusFailed, commonv1alpha1.DownloadFailureMissingArticles
	jc.items["job-1"] = it
	jc.mu.Unlock()
	require.NoError(t, rep.Resync(ctx, 1))
	handle(cmd(1, schema.EngineDesiredAbsent)) // stale: below the journal's seq

	assert.Empty(t, jc.removeCalls, "no removal without an absent command above the journal's seq")
	_, err = jc.Get(ctx, "job-1")
	require.NoError(t, err)
	assert.Equal(t, schema.TransferStateFailed, record().State, "the failure is reported, the decision left to the manager")

	absent := cmd(3, schema.EngineDesiredAbsent)
	absent.RemoveData = true
	handle(absent)
	require.Len(t, jc.removeCalls, 1)
	assert.Equal(t, removeCall{id: "job-1", deleteData: true}, jc.removeCalls[0])
	assert.Equal(t, schema.TransferStateRemoved, record().State)
}
