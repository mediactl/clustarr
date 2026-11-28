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

package maintenance_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/maintenance"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/segments"
	"github.com/mediactl/clustarr/pkg/segments/fingerprintmeta"
)

// fillUntilRefused writes 64 KiB objects named <prefix>NNN to store until the
// broker refuses one for room, and returns the names it stored, oldest first.
func fillUntilRefused(t *testing.T, store events.ObjectStore, prefix string) []string {
	t.Helper()
	body := bytes.Repeat([]byte{0xA5}, 64<<10)
	var names []string
	for i := range 200 {
		name := fmt.Sprintf("%s%03d", prefix, i)
		_, err := store.Put(t.Context(), name, bytes.NewReader(body), events.ObjectMeta{})
		if err != nil {
			require.ErrorIs(t, err, events.ErrObjectStoreFull, "a bucket at its byte limit refuses a write as full")
			return names
		}
		names = append(names, name)
	}
	t.Fatal("the bucket never filled")
	return nil
}

// A cache store at its byte limit refuses every write (its stream discards
// new), which is where talos's fingerprint store stood on 2026-11-28. The
// fill check deletes its oldest-written objects until it is back at
// ObjectStoreEvictTo of the limit: the newest stay, the next write is taken,
// and the size and the limit are both reported. A store that is no cache
// keeps everything, and is only reported.
func TestACacheStoreNearlyFullGivesUpItsOldest(t *testing.T) {
	ctx := t.Context()
	bus, _ := natsBus(t)
	const limit = 2 << 20
	top := events.Default().ForSingleNode()
	top.ObjectStores = append(append([]events.ObjectStoreSpec(nil), top.ObjectStores...),
		events.ObjectStoreSpec{Name: "clustarr-test-cache", Storage: events.StorageFile, MaxBytes: limit, Replicas: 1, Cache: true},
		events.ObjectStoreSpec{Name: "clustarr-test-kept", Storage: events.StorageFile, MaxBytes: limit, Replicas: 1})
	require.NoError(t, bus.Ensure(ctx, top))
	cache, kept := bus.ObjectStore("clustarr-test-cache"), bus.ObjectStore("clustarr-test-kept")
	cached := fillUntilRefused(t, cache, "c")
	held := fillUntilRefused(t, kept, "k")
	require.Greater(t, len(cached), 10)

	m := &maintenance.ObjectStores{Topology: top, Stores: bus.ObjectStore}
	require.NoError(t, m.CheckFill(ctx, time.Now()))

	st, err := cache.Status(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, limit, st.MaxBytes, "a store's status carries its byte limit")
	assert.Less(t, float64(st.Bytes), events.ObjectStoreNearlyFull*limit, "the cache is no longer nearly full")
	left, err := cache.List(ctx, "")
	require.NoError(t, err)
	require.NotEmpty(t, left)
	require.Less(t, len(left), len(cached))
	// What is left is the newest: the names written last, with no gap.
	want := cached[len(cached)-len(left):]
	got := make([]string, 0, len(left))
	for _, o := range left {
		got = append(got, o.Name)
	}
	assert.ElementsMatch(t, want, got, "the oldest-written objects are the ones evicted")
	_, err = cache.Put(ctx, "after", bytes.NewReader(bytes.Repeat([]byte{1}, 64<<10)), events.ObjectMeta{})
	require.NoError(t, err, "a write is taken again")

	all, err := kept.List(ctx, "")
	require.NoError(t, err)
	assert.Len(t, all, len(held), "a store that is no cache loses nothing")

	// Below the mark nothing more goes.
	require.NoError(t, m.CheckFill(ctx, time.Now()))
	again, err := cache.List(ctx, "")
	require.NoError(t, err)
	assert.Len(t, again, len(left)+1)
}

// The fingerprint reap deletes what no file reads: a fingerprint whose probe
// hash no file has or had, one of a version the analyzer no longer reads,
// and a name that is no fingerprint key. A file's fingerprints, under the
// hash it has or one it had, and anything younger than the grace stay.
// Nothing is deleted when the lookup fails, or when it answers no for a
// whole store.
func TestFingerprintsNoFileNamesAreReaped(t *testing.T) {
	ctx := t.Context()
	bus, _ := natsBus(t)
	top := events.Default().ForSingleNode()
	require.NoError(t, bus.Ensure(ctx, top))
	store := bus.ObjectStore(events.ObjectStoreFingerprints)
	put := func(name string) {
		t.Helper()
		_, err := store.Put(ctx, name, bytes.NewReader([]byte("fingerprint")), events.ObjectMeta{})
		require.NoError(t, err)
	}
	names := func() []string {
		t.Helper()
		left, err := store.List(ctx, "")
		require.NoError(t, err)
		var out []string
		for _, o := range left {
			out = append(out, o.Name)
		}
		return out
	}
	key := segments.FingerprintKey
	all := []string{
		key("current", "start"), key("current", "end"),
		key("former", "start"), // under a hash a file had
		key("orphan", "start"), key("orphan", "end"),
		"current.start", // the legacy, unversioned key of a current file
		"notes",         // no fingerprint key at all
	}
	for _, n := range all {
		put(n)
	}
	asked := map[string]int{}
	referenced := func(_ context.Context, h string) (bool, error) {
		asked[h]++
		return h == "current" || h == "former", nil
	}
	now := time.Now()

	n, err := fingerprintmeta.ReapStore(ctx, store, referenced, time.Hour, now)
	require.NoError(t, err)
	assert.Zero(t, n, "nothing younger than the grace is reaped")

	failed := errors.New("no library")
	_, err = fingerprintmeta.ReapStore(ctx, store, func(context.Context, string) (bool, error) { return false, failed },
		time.Minute, now.Add(time.Hour))
	require.ErrorIs(t, err, failed)
	assert.ElementsMatch(t, all, names(), "a lookup that fails deletes nothing")

	clear(asked)
	n, err = fingerprintmeta.ReapStore(ctx, store, referenced, time.Minute, now.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.ElementsMatch(t, []string{key("current", "start"), key("current", "end"), key("former", "start")}, names())
	assert.Equal(t, map[string]int{"current": 1, "former": 1, "orphan": 1}, asked, "each hash is asked once, whatever its windows")

	// A lookup that answers no for a whole store is a broken lookup.
	for i := range 20 {
		put(key(fmt.Sprintf("h%02d", i), "start"))
	}
	before := names()
	_, err = fingerprintmeta.ReapStore(ctx, store, func(context.Context, string) (bool, error) { return false, nil },
		time.Nanosecond, time.Now().Add(time.Hour))
	require.ErrorIs(t, err, fingerprintmeta.ErrNoRefs)
	assert.ElementsMatch(t, before, names())

	// The loop runs it as a Reap on every sweep and carries on past one
	// that fails.
	m := &maintenance.ObjectStores{Topology: top, Grace: time.Nanosecond, Reaps: []maintenance.Reap{
		{Name: "broken", Run: func(context.Context, time.Duration) (int, error) { return 0, failed }},
		{Name: events.ObjectStoreFingerprints, Run: func(ctx context.Context, grace time.Duration) (int, error) {
			return fingerprintmeta.ReapStore(ctx, store, referenced, grace, time.Now().Add(time.Hour))
		}},
	}}
	require.ErrorIs(t, m.Sweep(ctx), failed)
	assert.ElementsMatch(t, []string{key("current", "start"), key("current", "end"), key("former", "start")}, names())
}
