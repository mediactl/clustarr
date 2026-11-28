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

package download

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// An indexer that serves each NZB once and refuses it for a day after, as
// nzb-life does: the second request for a release -- the reply was lost, the
// engine restarted -- is answered from what the first fetch left, and the
// indexer is not asked. A .torrent is never kept (it carries a passkey), and
// a kept object that is no .nzb is removed and the indexer asked.
func TestAFetchedNZBIsKeptAndTheIndexerAskedOnce(t *testing.T) {
	ctx := context.Background()
	idx := testIndexer("media", "nzb-life", "uid-kept", indexv1alpha1.LimitUnitDay)
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	store := bus.ObjectStore(events.BucketPayloads)
	now := time.Date(2026, 11, 28, 2, 0, 0, 0, time.UTC)

	const nzb = `<?xml version="1.0"?><nzb><file subject="one"/></nzb>`
	fetched := map[string]int{}
	s := &Service{
		Client: fakeClient(t, idx), Bus: bus, Payloads: store, Now: func() time.Time { return now },
		Fetch: func(context.Context, *indexv1alpha1.Indexer) (Fetcher, error) {
			return fetcherFunc(func(_ context.Context, rawURL string) (*FetchResult, error) {
				fetched[rawURL]++
				switch {
				case strings.HasSuffix(rawURL, "/torrent"):
					return &FetchResult{
						Status: http.StatusOK, ContentLen: -1,
						Body: io.NopCloser(strings.NewReader("d8:announce40:https://tracker.example/PASSKEY/announcee")),
					}, nil
				case fetched[rawURL] > 1:
					return &FetchResult{
						Status: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"86400"}},
						Body: io.NopCloser(strings.NewReader(`<h1>Already Downloaded</h1>`)),
					}, nil
				}
				return &FetchResult{
					Status: http.StatusOK, Header: http.Header{"Content-Type": {"application/x-nzb"}},
					ContentLen: -1, Body: io.NopCloser(strings.NewReader(nzb)),
				}, nil
			}), nil
		},
	}
	req := func(guid, u string) schema.DownloadRequest {
		return schema.DownloadRequest{IndexerRef: schema.Ref{Namespace: "media", Name: "nzb-life"}, GUID: guid, URL: u}
	}

	first := s.Handle(ctx, req("https://x/getnzb?id=1&apikey=SECRET", "https://x/nzb"))
	require.Empty(t, first.Error)
	require.Equal(t, nzb, string(first.Bytes))

	again := s.Handle(ctx, req("https://x/getnzb?id=1&apikey=SECRET", "https://x/nzb"))
	require.Empty(t, again.Error, "the second request is answered, not refused: %q", again.Error)
	require.Equal(t, nzb, string(again.Bytes))
	require.Equal(t, nzbContentType, again.ContentType)
	require.Equal(t, 1, fetched["https://x/nzb"], "the indexer is asked for a release once")

	// The object's name carries neither the GUID nor anything of it.
	kept, err := store.List(ctx, "")
	require.NoError(t, err)
	require.Len(t, kept, 1)
	require.Equal(t, PayloadKey(idx.UID, "https://x/getnzb?id=1&apikey=SECRET"), kept[0].Name)
	require.NotContains(t, kept[0].Name, "SECRET")
	require.NotContains(t, kept[0].Name, "getnzb")
	require.Equal(t, events.PayloadsMetaVersion, kept[0].Metadata[events.ObjectMetaKeyVersion])

	// A .torrent is handed over and never kept.
	for range 2 {
		tor := s.Handle(ctx, req("g-torrent", "https://x/torrent"))
		require.Empty(t, tor.Error)
	}
	require.Equal(t, 2, fetched["https://x/torrent"])
	kept, err = store.List(ctx, "")
	require.NoError(t, err)
	require.Len(t, kept, 1, "only the .nzb is kept")

	// A kept object that does not read back as an .nzb is removed, and the
	// indexer is asked (which here refuses: the answer is the indexer's).
	_, err = store.Put(ctx, kept[0].Name, bytes.NewReader([]byte("not gzip")), payloadMeta())
	require.NoError(t, err)
	bad := s.Handle(ctx, req("https://x/getnzb?id=1&apikey=SECRET", "https://x/nzb"))
	require.NotEmpty(t, bad.Error)
	require.Equal(t, 2, fetched["https://x/nzb"])
	_, err = store.Info(ctx, kept[0].Name)
	require.ErrorIs(t, err, events.ErrObjectNotFound)
}
