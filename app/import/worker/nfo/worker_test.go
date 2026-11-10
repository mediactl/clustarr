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

package nfo_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/import/worker/nfo"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

type fakeMessage struct {
	env        *events.Envelope
	inProgress int
}

func (m *fakeMessage) Ack(context.Context) error                { return nil }
func (m *fakeMessage) Nak(context.Context, time.Duration) error { return nil }
func (m *fakeMessage) Term(context.Context, string) error       { return nil }
func (m *fakeMessage) InProgress(context.Context) error         { m.inProgress++; return nil }
func (m *fakeMessage) Envelope() *events.Envelope               { return m.env }
func (m *fakeMessage) Subject() string                          { return "clustarr.work.import.nfo.album.u1" }
func (m *fakeMessage) Attempt() uint64                          { return 1 }

func message(t *testing.T, task schema.NFOWriteTask) *fakeMessage {
	t.Helper()
	name, data, err := schema.Encode(task)
	require.NoError(t, err)
	return &fakeMessage{env: &events.Envelope{ID: "nfo/u1/1", Type: "import.NFOWriteTask", Schema: name, Data: data}}
}

func setup(t *testing.T) (context.Context, *membus.Bus, *nfo.Worker, string, *records.Requester[*schema.NFORecord]) {
	t.Helper()
	ctx := context.Background()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	dataDir := t.TempDir()
	w := nfo.New(bus, dataDir, "import-agent-0")
	req := records.NewRequester(bus.KV(events.BucketNFO), agentrecords.NFO())
	return ctx, bus, w, dataDir, req
}

var item = schema.ItemRef{Kind: "Album", Ref: schema.Ref{Namespace: "media", Name: "muse-absolution", UID: "u1"}}

func request(t *testing.T, ctx context.Context, req *records.Requester[*schema.NFORecord], seq int64, rev uint64, opts ...records.Option) uint64 {
	t.Helper()
	rec := &schema.NFORecord{Request: schema.NFOWriteRequest{Hash: "h"}}
	h := rec.Header()
	h.Schema, h.Item, h.Seq = schema.NFORecord{}.Schema(), &item, seq
	rev, err := req.Request(ctx, agentrecords.NFOKey(item.UID), rev, rec, opts...)
	require.NoError(t, err)
	return rev
}

// The worker writes the manager's bytes and the artwork object into the
// folder, keeps an operator's own file, and answers the record with what
// it wrote; a later write of the same names replaces only its own.
func TestWritesDocumentsAndImagesAndAnswers(t *testing.T) {
	ctx, bus, w, dataDir, req := setup(t)
	folder := filepath.Join(dataDir, "music", "Muse", "Absolution")
	require.NoError(t, os.MkdirAll(folder, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "folder.jpg"), []byte("the operator's cover"), 0o644))
	_, err := bus.ObjectStore(events.BucketArtwork).Put(ctx, "album/u1/poster", bytes.NewReader([]byte("JPEGBYTES")), events.ObjectMeta{})
	require.NoError(t, err)
	rev := request(t, ctx, req, 1, 0)

	task := schema.NFOWriteTask{
		Item: item, Folder: folder, Hash: "h", Seq: 1,
		Files:  []schema.NFOFile{{Name: "album.nfo", Content: []byte("<album/>")}},
		Images: []schema.NFOImage{{Name: "folder.jpg", Object: "album/u1/poster"}, {Name: "disc.jpg", Object: "album/u1/poster"}},
	}
	require.NoError(t, w.Handle(ctx, message(t, task)))

	got, err := os.ReadFile(filepath.Join(folder, "album.nfo"))
	require.NoError(t, err)
	assert.Equal(t, "<album/>", string(got))
	kept, err := os.ReadFile(filepath.Join(folder, "folder.jpg"))
	require.NoError(t, err)
	assert.Equal(t, "the operator's cover", string(kept), "a file the worker did not write is never replaced")
	disc, err := os.ReadFile(filepath.Join(folder, "disc.jpg"))
	require.NoError(t, err)
	assert.Equal(t, "JPEGBYTES", string(disc))
	info, err := os.Stat(filepath.Join(folder, "album.nfo"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o664), info.Mode().Perm(), "the default mode is the RootFolder's 0664")

	rec, rev2, ok, err := req.Get(ctx, agentrecords.NFOKey(item.UID))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Greater(t, rev2, rev)
	assert.Equal(t, records.StateAnswered, rec.State)
	require.NotNil(t, rec.Answer)
	assert.Equal(t, []string{"album.nfo", "disc.jpg"}, rec.Answer.Files)
	assert.Equal(t, []string{"folder.jpg"}, rec.Answer.Kept)
	assert.Equal(t, "h", rec.Request.Hash)
	assert.Equal(t, "import-agent-0", rec.Writer)

	// A second request (the loop having incorporated the first answer, a
	// fact) rewrites the worker's own files and still keeps the operator's.
	request(t, ctx, req, 2, rev2, records.IncorporatedThrough(1))
	task.Seq = 2
	task.Owned = rec.Answer.Files // what the loop recorded in status.nfo.files
	task.Files[0].Content = []byte("<album><title>Absolution</title></album>")
	require.NoError(t, w.Handle(ctx, message(t, task)))
	got, err = os.ReadFile(filepath.Join(folder, "album.nfo"))
	require.NoError(t, err)
	assert.Contains(t, string(got), "Absolution")
	kept, _ = os.ReadFile(filepath.Join(folder, "folder.jpg"))
	assert.Equal(t, "the operator's cover", string(kept))
}

// A task whose record moved on (a newer request) is dropped without a
// write; a folder outside the data directory, or a file name carrying a
// path, is dead-lettered without a write.
func TestRefusals(t *testing.T) {
	ctx, _, w, dataDir, req := setup(t)
	folder := filepath.Join(dataDir, "music", "X")
	request(t, ctx, req, 2, 0)
	stale := schema.NFOWriteTask{Item: item, Folder: folder, Hash: "h", Seq: 1, Files: []schema.NFOFile{{Name: "album.nfo", Content: []byte("x")}}}
	require.NoError(t, w.Handle(ctx, message(t, stale)))
	_, err := os.Stat(filepath.Join(folder, "album.nfo"))
	assert.True(t, os.IsNotExist(err), "a superseded task writes nothing")

	outside := schema.NFOWriteTask{Item: item, Folder: filepath.Join(dataDir, "..", "elsewhere"), Hash: "h", Seq: 2, Files: []schema.NFOFile{{Name: "album.nfo", Content: []byte("x")}}}
	err = w.Handle(ctx, message(t, outside))
	require.Error(t, err)
	var discard *events.DiscardError
	assert.True(t, errors.As(err, &discard), "a folder outside /data is dead-lettered, not retried: %v", err)

	escaping := schema.NFOWriteTask{Item: item, Folder: folder, Hash: "h", Seq: 2, Files: []schema.NFOFile{{Name: "../album.nfo", Content: []byte("x")}}}
	err = w.Handle(ctx, message(t, escaping))
	require.Error(t, err)
	_, statErr := os.Stat(filepath.Join(dataDir, "music", "album.nfo"))
	assert.True(t, os.IsNotExist(statErr))
	rec, _, ok, err := req.Get(ctx, agentrecords.NFOKey(item.UID))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, records.StateFailed, rec.State, "the record says why")
	assert.Contains(t, rec.Failure, "not a bare name")
}
