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

package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/plexextras"
)

const extrasPlexID = "5d776b83fb0d55001f56a04b"

// fakeExtras is a Plex client whose answer the test sets, counting how
// often Plex was asked.
type fakeExtras struct {
	mu     sync.Mutex
	calls  int
	extras []json.RawMessage
	err    error
}

func (f *fakeExtras) Name() string { return "plex" }

func (f *fakeExtras) Capabilities() pkgmetadata.Capabilities {
	return pkgmetadata.Capabilities{}
}

func (f *fakeExtras) ShowChildren(context.Context, pkgmetadata.ExternalIDs) (*pkgmetadata.PlexChildren, error) {
	return nil, pkgmetadata.ErrNotFound
}

func (f *fakeExtras) Extras(_ context.Context, plexID string) ([]json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.extras, nil
}

func (f *fakeExtras) set(extras []json.RawMessage, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.extras, f.err = extras, err
}

func (f *fakeExtras) asked() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

var teaser = json.RawMessage(`{"title":"Teaser Trailer","subtype":"trailer"}`)

type extrasHarness struct {
	bus   events.Bus
	clock *clockwork.FakeClock
	plex  *fakeExtras
}

func newExtrasHarness(t *testing.T) extrasHarness {
	t.Helper()
	clock := clockwork.NewFakeClockAt(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	bus := newTestBus(t)
	plex := &fakeExtras{extras: []json.RawMessage{teaser}}
	reg := &pkgmetadata.Registry{Plex: []pkgmetadata.PlexProvider{plex}}
	require.NoError(t, ServeExtras(bus, bus.KV(events.BucketPlexExtras), reg, clock))
	return extrasHarness{bus: bus, clock: clock, plex: plex}
}

func (h extrasHarness) ask(t *testing.T, plexID string) schema.PlexExtrasResponse {
	t.Helper()
	var resp schema.PlexExtrasResponse
	require.NoError(t, h.bus.Request(context.Background(), events.RPCMetadataExtras, schema.PlexExtrasRequest{PlexID: plexID}, &resp))
	return resp
}

// A miss is fetched from Plex once and stored; the next request is answered
// from the bucket without asking Plex again.
func TestExtrasAreFetchedOnAMissAndServedFromTheBucketAfter(t *testing.T) {
	h := newExtrasHarness(t)

	first := h.ask(t, extrasPlexID)
	require.Empty(t, first.Error)
	require.Len(t, first.Extras, 1)
	require.JSONEq(t, string(teaser), string(first.Extras[0]))

	e, err := h.bus.KV(events.BucketPlexExtras).Get(context.Background(), plexextras.Key(extrasPlexID))
	require.NoError(t, err)
	stored, err := plexextras.Decode(e.Value)
	require.NoError(t, err)
	require.True(t, h.clock.Now().Equal(stored.FetchedAt))
	require.Len(t, stored.Extras, 1)

	h.plex.set(nil, errors.New("plex must not be asked again"))
	second := h.ask(t, extrasPlexID)
	require.Empty(t, second.Error)
	require.Len(t, second.Extras, 1)
	require.Equal(t, 1, h.plex.asked())
}

// An entry older than the freshness is fetched again and replaced.
func TestAStaleEntryIsFetchedAgain(t *testing.T) {
	h := newExtrasHarness(t)
	h.ask(t, extrasPlexID)

	h.clock.Advance(extrasFreshness + time.Minute)
	h.plex.set([]json.RawMessage{teaser, teaser}, nil)
	got := h.ask(t, extrasPlexID)
	require.Empty(t, got.Error)
	require.Len(t, got.Extras, 2)
	require.Equal(t, 2, h.plex.asked())
	require.True(t, h.clock.Now().Equal(got.FetchedAt))
}

// When Plex cannot be asked, a stale entry is still the answer: a failed
// refetch must never become an empty list, which PMS reads as "delete".
func TestAStaleEntryIsServedWhenPlexFails(t *testing.T) {
	h := newExtrasHarness(t)
	h.ask(t, extrasPlexID)
	fetched := h.clock.Now()

	h.clock.Advance(extrasFreshness + time.Minute)
	h.plex.set(nil, errors.New("plex.tv: 503"))
	got := h.ask(t, extrasPlexID)
	require.Empty(t, got.Error)
	require.Len(t, got.Extras, 1)
	require.True(t, fetched.Equal(got.FetchedAt), "the stale entry's own fetch time")
}

// A miss Plex cannot answer is an error, never an empty list.
func TestAMissPlexCannotAnswerIsAnError(t *testing.T) {
	h := newExtrasHarness(t)
	h.plex.set(nil, errors.New("plex.tv: 503"))
	got := h.ask(t, extrasPlexID)
	require.NotEmpty(t, got.Error)
	require.Nil(t, got.Extras)

	_, err := h.bus.KV(events.BucketPlexExtras).Get(context.Background(), plexextras.Key(extrasPlexID))
	require.ErrorIs(t, err, events.ErrKeyNotFound, "a failure stores nothing")
}

// "None" is an answer Plex gives: it is stored and served as an empty
// list, so a library refresh does not ask Plex again for every item
// without trailers.
func TestNoExtrasAreStoredAndServedAsAnEmptyList(t *testing.T) {
	h := newExtrasHarness(t)
	h.plex.set([]json.RawMessage{}, nil)
	first := h.ask(t, extrasPlexID)
	require.Empty(t, first.Error)
	require.NotNil(t, first.Extras)
	require.Empty(t, first.Extras)

	second := h.ask(t, extrasPlexID)
	require.Empty(t, second.Error)
	require.NotNil(t, second.Extras)
	require.Equal(t, 1, h.plex.asked())
}

func TestExtrasWithoutAPlexProviderAreAnError(t *testing.T) {
	bus := newTestBus(t)
	require.NoError(t, ServeExtras(bus, bus.KV(events.BucketPlexExtras), &pkgmetadata.Registry{}, clockwork.NewRealClock()))
	var resp schema.PlexExtrasResponse
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataExtras, schema.PlexExtrasRequest{PlexID: extrasPlexID}, &resp))
	require.Contains(t, resp.Error, "no plex metadata provider")
}

func TestExtrasRefuseAnEmptyPlexID(t *testing.T) {
	h := newExtrasHarness(t)
	got := h.ask(t, "")
	require.NotEmpty(t, got.Error)
	require.Equal(t, 0, h.plex.asked())
}
