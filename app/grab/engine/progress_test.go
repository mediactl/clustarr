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
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/engine"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// countingKV is an events.KV that keeps the last value per key and counts
// the writes, which is what "bounded writes" is about.
type countingKV struct {
	mu      sync.Mutex
	values  map[string][]byte
	puts    int
	deletes int
}

func newCountingKV() *countingKV { return &countingKV{values: map[string][]byte{}} }

func (k *countingKV) Get(_ context.Context, key string) (events.Entry, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.values[key]
	if !ok {
		return events.Entry{}, events.ErrKeyNotFound
	}
	return events.Entry{Key: key, Value: v}, nil
}

func (k *countingKV) Create(context.Context, string, []byte, ...events.KVOption) (uint64, error) {
	panic("the progress publisher only puts")
}

func (k *countingKV) Update(context.Context, string, []byte, uint64) (uint64, error) {
	panic("the progress publisher only puts")
}

func (k *countingKV) Put(_ context.Context, key string, val []byte) (uint64, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.puts++
	k.values[key] = val
	return uint64(k.puts), nil
}

func (k *countingKV) Delete(_ context.Context, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.deletes++
	delete(k.values, key)
	return nil
}

func (k *countingKV) DeleteRevision(context.Context, string, uint64) error {
	panic("the progress publisher only puts")
}

func (k *countingKV) Watch(context.Context, string, ...events.WatchOption) (<-chan events.Entry, error) {
	panic("the progress publisher only puts")
}

func (k *countingKV) Keys(context.Context) ([]string, error) {
	panic("the progress publisher only puts")
}

func (k *countingKV) Status(context.Context) (events.KVStatus, error) {
	panic("the progress publisher only puts")
}

// listTransfers is an engine.Transfers whose only live method is List.
type listTransfers struct {
	mu   sync.Mutex
	held []engine.Held
}

func (l *listTransfers) set(held ...engine.Held) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held = held
}

func (l *listTransfers) List(context.Context) ([]engine.Held, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]engine.Held(nil), l.held...), nil
}

func (l *listTransfers) Apply(context.Context, schema.EngineCommand, *engine.Held) (engine.Held, error) {
	panic("the progress publisher only lists")
}

func (l *listTransfers) RemoveByID(context.Context, string, bool) error {
	panic("the progress publisher only lists")
}

// journalled is a transfer the client holds whose journal names its entry
// (name, uid), claimed by a Movie in "media".
func journalled(name, uid string, item download.Item) engine.Held {
	return engine.Held{
		Name: name,
		Journal: engine.Journal{
			EntryUID: uid,
			Claim: &schema.TransferClaim{
				Owner: schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: "media", Name: "m"}},
				Entry: schema.EntryRef{ID: name, UID: uid},
			},
		},
		Item:  item,
		Found: true,
	}
}

// One write per change, none for an unchanged sample, none for a transfer
// whose journal names no entry (a pre-journal transfer) or that the client
// no longer holds, and a delete when a transfer leaves -- the bound that
// keeps a 1 Hz sampler from putting every transfer into a replicated bucket
// every second.
func TestProgressPublisherWritesOnlyWhatChanged(t *testing.T) {
	ctx := context.Background()
	kv := newCountingKV()
	lt := &listTransfers{}

	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	p := &engine.ProgressPublisher{
		Transfers: lt, EngineID: "torrents-0", KV: kv,
		Now: func() time.Time { return now },
	}
	eta := 90 * time.Second
	movingItem := download.Item{
		ID: "aaaa", Status: download.StatusDownloading, Stage: commonv1alpha1.DownloadStageTransferring,
		TotalBytes: 3000, DownloadedBytes: 1000, DownRate: 500, ETA: &eta, Peers: 4, Seeders: 2,
	}
	moving := journalled("moving", "uid-moving", movingItem)
	idle := journalled("idle", "uid-idle", download.Item{ID: "bbbb", Status: download.StatusPaused, TotalBytes: 10, DownloadedBytes: 5})
	// A pre-journal transfer: no entry names it until a command does.
	orphan := engine.Held{Name: "orphan", Item: download.Item{ID: "dddd", Status: download.StatusDownloading}, Found: true}
	// A journalled transfer the client no longer holds.
	gone := journalled("gone", "uid-gone", download.Item{ID: "cccc", Status: download.StatusDownloading})
	gone.Found = false
	lt.set(moving, idle, orphan, gone)

	require.NoError(t, p.PublishOnce(ctx))
	assert.Equal(t, 2, kv.puts, "the first sample writes each matched transfer once, and only those")

	raw, err := kv.Get(ctx, engine.ProgressKey("uid-moving"))
	require.NoError(t, err)
	var got schema.DownloadProgress
	require.NoError(t, json.Unmarshal(raw.Value, &got))
	assert.Equal(t, schema.DownloadProgress{
		DownloadRef:         schema.Ref{Namespace: "media", Name: "moving", UID: "uid-moving"},
		Status:              "Downloading",
		Stage:               "transferring",
		PercentMilli:        33_333,
		TotalBytes:          3000,
		DownloadedBytes:     1000,
		DownRateBytesPerSec: 500,
		ETASeconds:          90,
		Seeders:             2,
		Peers:               4,
		At:                  now,
	}, got)

	now = now.Add(time.Second)
	require.NoError(t, p.PublishOnce(ctx))
	assert.Equal(t, 2, kv.puts, "nothing changed, so nothing is written")

	now = now.Add(time.Second)
	moving.Item.DownloadedBytes = 2000
	lt.set(moving, idle, orphan, gone)
	require.NoError(t, p.PublishOnce(ctx))
	assert.Equal(t, 3, kv.puts, "only the transfer that moved is written")

	now = now.Add(100 * time.Millisecond)
	moving.Item.DownloadedBytes = 2500
	lt.set(moving, idle, orphan, gone)
	require.NoError(t, p.PublishOnce(ctx))
	assert.Equal(t, 3, kv.puts, "a second sample inside the interval waits for the next tick")

	now = now.Add(time.Second)
	lt.set(idle, orphan, gone)
	require.NoError(t, p.PublishOnce(ctx))
	assert.Equal(t, 1, kv.deletes, "a transfer that left the client takes its key with it")
	_, err = kv.Get(ctx, engine.ProgressKey("uid-moving"))
	assert.ErrorIs(t, err, events.ErrKeyNotFound)
}

func TestProgressPublisherWaitsForReAttach(t *testing.T) {
	kv := newCountingKV()
	lt := &listTransfers{}
	lt.set(journalled("a", "uid-a", download.Item{ID: "aaaa"}))
	p := &engine.ProgressPublisher{
		Transfers: lt, EngineID: "t-0", KV: kv,
		Ready: func() bool { return false },
	}
	require.NoError(t, p.PublishOnce(context.Background()))
	assert.Zero(t, kv.puts)
}
