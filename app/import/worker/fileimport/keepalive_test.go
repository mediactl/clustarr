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
package fileimport

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// beats is a delivery that counts its in-progress acks.
type beats struct{ n atomic.Int32 }

func (*beats) Envelope() *events.Envelope               { return &events.Envelope{} }
func (*beats) Subject() string                          { return "" }
func (*beats) Attempt() uint64                          { return 1 }
func (*beats) Ack(context.Context) error                { return nil }
func (*beats) Nak(context.Context, time.Duration) error { return nil }
func (*beats) Term(context.Context, string) error       { return nil }
func (b *beats) InProgress(context.Context) error       { b.n.Add(1); return nil }

// A step that outlasts the delivery's ack deadline -- a join or a copy of a
// whole film -- keeps the delivery alive while it runs: on 2026-10-17 four
// of five joins ran past it, were redelivered and ran a second time beside
// the first, and 49 Up was placed half joined.
func TestALongImportStepKeepsItsDeliveryAlive(t *testing.T) {
	w := &Worker{HeartbeatEvery: 5 * time.Millisecond}
	m := &beats{}
	err := w.keepAlive(context.Background(), m, func() error {
		time.Sleep(60 * time.Millisecond)
		return nil
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, m.n.Load(), int32(3), "beats while the step runs")
	n := m.n.Load()
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, n, m.n.Load(), "and none once it has returned")
}

// Two joins of the same parts never write one file: a redelivered execute
// running beside the first wrote into the first's join file (2026-10-17).
func TestTwoJoinsOfOnePartSetWriteTwoFiles(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "library")
	require.NoError(t, os.MkdirAll(root, 0o755))
	parts := []string{filepath.Join(dir, "Film.Part1.mkv"), filepath.Join(dir, "Film.Part2.mkv")}
	for _, p := range parts {
		require.NoError(t, os.WriteFile(p, []byte("part"), 0o644))
	}
	var mu sync.Mutex
	var outs []string
	w := &Worker{Join: func(_ context.Context, _ []string, out string) error {
		mu.Lock()
		outs = append(outs, out)
		mu.Unlock()
		return os.WriteFile(out, []byte("joined"), 0o644)
	}}
	plan := schema.ImportPlan{RootFolder: root, RecycleBin: filepath.Join(dir, "recycle")}
	dest := filepath.Join(root, "Film (2020)", "Film (2020).mkv")
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
	for range 2 {
		refused, why, err := w.joinPlace(context.Background(), plan, parts, dest)
		require.NoError(t, err, why)
		require.Empty(t, refused, why)
	}
	require.Len(t, outs, 2)
	assert.NotEqual(t, outs[0], outs[1], "each join its own file")
}

// An execute is kept alive from its first step to its record, whatever its
// steps: a join, a copy, a donor placed and reduced, the replaced files
// recycled (2026-10-17: only the walk beat, and a long step was redelivered
// beside itself).
func TestAnExecuteKeepsItsDeliveryAliveThroughout(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	root := filepath.Join(dir, "library")
	dest := filepath.Join(root, "Film (2020)", "Film (2020).mkv")
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
	parts := []string{filepath.Join(dir, "Film.Part1.mkv"), filepath.Join(dir, "Film.Part2.mkv")}
	for _, p := range parts {
		require.NoError(t, os.WriteFile(p, []byte("part"), 0o644))
	}
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	w := &Worker{Bus: bus, HeartbeatEvery: 5 * time.Millisecond, Join: func(_ context.Context, _ []string, out string) error {
		time.Sleep(60 * time.Millisecond)
		return os.WriteFile(out, []byte("joined"), 0o644)
	}}
	owner := schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: "media", Name: "film", UID: "film-uid"}}
	task := schema.ImportExecuteTask{
		Seq: 1, Owner: owner, Entry: schema.EntryRef{ID: "film-a", UID: "entry-a"},
		Plan: schema.ImportPlan{
			Mode: schema.ImportModeMove, RootFolder: root, RecycleBin: filepath.Join(dir, "recycle"),
			Moves: []schema.ImportMove{{Source: parts[0], Dest: dest, Target: owner, Join: parts, MediaFileName: "film-mf"}},
		},
	}
	name, data, err := schema.Encode(task)
	require.NoError(t, err)
	m := &beats{}
	require.NoError(t, w.handleExecute(ctx, m, &events.Envelope{Schema: name, Data: data}))
	assert.GreaterOrEqual(t, m.n.Load(), int32(3), "beats through the execute")
}
