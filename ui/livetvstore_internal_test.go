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

package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
)

var tvKey = types.NamespacedName{Namespace: "media", Name: "argon-tv"}

// countingStore counts Gets and can be told to fail them.
type countingStore struct {
	events.ObjectStore
	gets atomic.Int32
	fail atomic.Bool
}

func (c *countingStore) Get(ctx context.Context, name string) (events.ObjectInfo, io.ReadCloser, error) {
	c.gets.Add(1)
	if c.fail.Load() {
		return events.ObjectInfo{}, nil, errors.New("down")
	}
	return c.ObjectStore.Get(ctx, name)
}

func liveTVBucket(t *testing.T) events.ObjectStore {
	t.Helper()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	return bus.ObjectStore(events.BucketLiveTV)
}

func putPlaylist(t *testing.T, s events.ObjectStore, names ...string) {
	t.Helper()
	pl := livetvstore.Playlist{}
	for _, n := range names {
		pl.Entries = append(pl.Entries, livetvstore.PlaylistEntry{Key: "n:" + n, Name: n, Group: "G", URL: "http://p/{username}/{password}/" + n + ".ts", UserAgent: "ua"})
	}
	var b bytes.Buffer
	require.NoError(t, livetvstore.Encode(&b, pl))
	_, err := s.Put(context.Background(), livetvstore.PlaylistObject(tvKey), &b, events.ObjectMeta{})
	require.NoError(t, err)
}

func TestTheStoreDecodesAnObjectOncePerDigest(t *testing.T) {
	cs := &countingStore{ObjectStore: liveTVBucket(t)}
	putPlaylist(t, cs, "ESPN", "CNN")
	clock := time.Unix(0, 0)
	l := newLiveTVStore(cs)
	l.now = func() time.Time { return clock }
	ctx := context.Background()

	v, ok := l.Playlist(ctx, tvKey)
	require.True(t, ok)
	require.Len(t, v.Entries, 2)
	_, ok = l.Playlist(ctx, tvKey)
	require.True(t, ok)
	assert.Equal(t, int32(1), cs.gets.Load(), "a second read within the recheck is the memo")

	clock = clock.Add(time.Minute)
	_, _ = l.Playlist(ctx, tvKey)
	assert.Equal(t, int32(1), cs.gets.Load(), "an unchanged digest is not read again")

	putPlaylist(t, cs, "ESPN", "CNN", "HBO")
	clock = clock.Add(time.Minute)
	v, ok = l.Playlist(ctx, tvKey)
	require.True(t, ok)
	assert.Len(t, v.Entries, 3)
	assert.Equal(t, int32(2), cs.gets.Load(), "a new digest is read once")
	assert.Equal(t, 2, v.ByKey["n:HBO"])
	assert.Equal(t, []string{"G"}, v.Groups)
}

func TestAStoreThatCannotBeReadKeepsTheLastGoodView(t *testing.T) {
	cs := &countingStore{ObjectStore: liveTVBucket(t)}
	putPlaylist(t, cs, "ESPN")
	clock := time.Unix(0, 0)
	l := newLiveTVStore(cs)
	l.now = func() time.Time { return clock }
	_, ok := l.Playlist(context.Background(), tvKey)
	require.True(t, ok)

	putPlaylist(t, cs, "ESPN", "CNN")
	cs.fail.Store(true)
	clock = clock.Add(time.Minute)
	v, ok := l.Playlist(context.Background(), tvKey)
	require.True(t, ok)
	assert.Len(t, v.Entries, 1, "the last good view")
}

func TestANilStoreAnswersNothing(t *testing.T) {
	l := newLiveTVStore(nil)
	_, ok := l.Playlist(context.Background(), tvKey)
	assert.False(t, ok)
	_, ok = l.GuideChannels(context.Background(), tvKey)
	assert.False(t, ok)
	_, ok = l.GuideIndex(context.Background(), tvKey)
	assert.False(t, ok)
	_, ok = newLiveTVStore(liveTVBucket(t)).GuideIndex(context.Background(), tvKey)
	assert.False(t, ok, "an object the tuner has not written")
}

func TestPlaylistEntriesCarryNoStreamURL(t *testing.T) {
	s := liveTVBucket(t)
	putPlaylist(t, s, "ESPN")
	v, ok := newLiveTVStore(s).Playlist(context.Background(), tvKey)
	require.True(t, ok)
	assert.Empty(t, v.Entries[0].URL)
	assert.Empty(t, v.Entries[0].UserAgent)
}

func TestTheGuideIndexViewIsByChannel(t *testing.T) {
	s := liveTVBucket(t)
	from := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	ix := livetvstore.GuideIndex{From: from,
		Channels: []livetvstore.IndexChannel{{Number: "1001", Name: "ESPN", Logo: "https://i/e.png"}, {Number: "1002", Name: "CNN"}},
		Programmes: []livetvstore.IndexProgramme{
			{Channel: "1001", Start: from, Stop: from.Add(time.Hour), Title: "A"},
			{Channel: "1001", Start: from.Add(time.Hour), Stop: from.Add(2 * time.Hour), Title: "B"},
			{Channel: "1002", Start: from, Stop: from.Add(time.Hour), Title: "C"},
		}}
	var b bytes.Buffer
	require.NoError(t, livetvstore.Encode(&b, ix))
	_, err := s.Put(context.Background(), livetvstore.GuideIndexObject(tvKey), &b, events.ObjectMeta{})
	require.NoError(t, err)
	v, ok := newLiveTVStore(s).GuideIndex(context.Background(), tvKey)
	require.True(t, ok)
	assert.Len(t, v.ByChannel["1001"], 2)
	assert.Equal(t, "https://i/e.png", v.Logos["1001"])
	assert.Equal(t, from, v.Index.From)
}
