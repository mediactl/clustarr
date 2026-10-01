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
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	usenetengine "github.com/mediactl/clustarr/app/grab/engine/usenet"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// fakeRequester is a minimal events.Requester test double, the same shape
// app/catalog/worker/search.FakeSearchRPC uses for events.RPCIndexSearch.
type fakeRequester struct {
	resp schema.DownloadResponse
	err  error

	lastSubject string
	lastReq     schema.DownloadRequest
}

func (f *fakeRequester) Request(_ context.Context, subject string, in, out any) error {
	f.lastSubject = subject
	if req, ok := in.(schema.DownloadRequest); ok {
		f.lastReq = req
	}
	if f.err != nil {
		return f.err
	}
	resp, ok := out.(*schema.DownloadResponse)
	if !ok {
		return errors.New("fakeRequester: unexpected out type")
	}
	*resp = f.resp
	return nil
}

func (f *fakeRequester) Serve(string, string, func(context.Context, []byte) ([]byte, error)) error {
	return errors.New("fakeRequester: Serve is not implemented")
}

func TestResolveNZBURLFetchesDirectly(t *testing.T) {
	body := []byte("<nzb>fixture</nzb>")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	url := srv.URL
	r := &usenetengine.Resolver{}
	got, err := r.Resolve(t.Context(), "media", downloadv1alpha1.DownloadSource{NZBURL: &url})
	require.NoError(t, err)
	assert.Equal(t, body, got)
}

func TestResolveNZBURLPropagatesNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	url := srv.URL
	r := &usenetengine.Resolver{}
	_, err := r.Resolve(t.Context(), "media", downloadv1alpha1.DownloadSource{NZBURL: &url})
	require.Error(t, err)
}

func TestResolveCapsResponseSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 100))
	}))
	defer srv.Close()

	url := srv.URL
	r := &usenetengine.Resolver{MaxBytes: 10}
	_, err := r.Resolve(t.Context(), "media", downloadv1alpha1.DownloadSource{NZBURL: &url})
	require.Error(t, err)
	assert.ErrorIs(t, err, usenetengine.ErrPayloadTooLarge)
}

func TestResolveIndexerDownloadUsesBytes(t *testing.T) {
	want := []byte("nzb-bytes")
	rpc := &fakeRequester{resp: schema.DownloadResponse{Bytes: want}}
	r := &usenetengine.Resolver{RPC: rpc}

	src := downloadv1alpha1.DownloadSource{
		IndexerDownload: &downloadv1alpha1.IndexerDownload{IndexerRef: "nzbgeek", GUID: "abc123"},
	}
	got, err := r.Resolve(t.Context(), "media", src)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, events.RPCIndexDownload, rpc.lastSubject)
	assert.Equal(t, "nzbgeek", rpc.lastReq.IndexerRef.Name)
	assert.Equal(t, "media", rpc.lastReq.IndexerRef.Namespace)
	assert.Equal(t, "abc123", rpc.lastReq.GUID)
}

func TestResolveIndexerDownloadFollowsRedirectURL(t *testing.T) {
	want := []byte("redirected-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(want)
	}))
	defer srv.Close()

	rpc := &fakeRequester{resp: schema.DownloadResponse{RedirectURL: srv.URL}}
	r := &usenetengine.Resolver{RPC: rpc}

	src := downloadv1alpha1.DownloadSource{
		IndexerDownload: &downloadv1alpha1.IndexerDownload{IndexerRef: "nzbgeek", GUID: "abc123"},
	}
	got, err := r.Resolve(t.Context(), "media", src)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestResolveIndexerDownloadRejectsMagnetURL(t *testing.T) {
	rpc := &fakeRequester{resp: schema.DownloadResponse{MagnetURL: "magnet:?xt=urn:btih:deadbeef"}}
	r := &usenetengine.Resolver{RPC: rpc}

	src := downloadv1alpha1.DownloadSource{
		IndexerDownload: &downloadv1alpha1.IndexerDownload{IndexerRef: "nzbgeek", GUID: "abc123"},
	}
	_, err := r.Resolve(t.Context(), "media", src)
	require.Error(t, err)
	assert.ErrorIs(t, err, usenetengine.ErrUnsupportedSource)
}

func TestResolveIndexerDownloadPropagatesNamedError(t *testing.T) {
	rpc := &fakeRequester{resp: schema.DownloadResponse{Error: "indexer rejected the guid"}}
	r := &usenetengine.Resolver{RPC: rpc}

	src := downloadv1alpha1.DownloadSource{
		IndexerDownload: &downloadv1alpha1.IndexerDownload{IndexerRef: "nzbgeek", GUID: "abc123"},
	}
	_, err := r.Resolve(t.Context(), "media", src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "indexer rejected the guid")
}

func TestResolveIndexerDownloadPropagatesRPCError(t *testing.T) {
	rpc := &fakeRequester{err: events.ErrNoResponders}
	r := &usenetengine.Resolver{RPC: rpc}

	src := downloadv1alpha1.DownloadSource{
		IndexerDownload: &downloadv1alpha1.IndexerDownload{IndexerRef: "nzbgeek", GUID: "abc123"},
	}
	_, err := r.Resolve(t.Context(), "media", src)
	require.Error(t, err)
	assert.ErrorIs(t, err, events.ErrNoResponders)
}

func TestResolveWithNoRPCConfiguredFailsRatherThanBlocking(t *testing.T) {
	r := &usenetengine.Resolver{}
	src := downloadv1alpha1.DownloadSource{
		IndexerDownload: &downloadv1alpha1.IndexerDownload{IndexerRef: "nzbgeek", GUID: "abc123"},
	}
	_, err := r.Resolve(t.Context(), "media", src)
	require.Error(t, err)
}

func TestResolveRejectsUnsupportedSource(t *testing.T) {
	torrentURL := "https://tracker.invalid/download/abc.torrent"
	r := &usenetengine.Resolver{}
	_, err := r.Resolve(t.Context(), "media", downloadv1alpha1.DownloadSource{TorrentURL: &torrentURL})
	require.Error(t, err)
	assert.ErrorIs(t, err, usenetengine.ErrUnsupportedSource)
}

// indexarr gzips a large .nzb for the bus (schema.DownloadResponse.ForWire);
// the engine reads it back through Payload, whole.
func TestResolveIndexerDownloadDecompressesAGzipReply(t *testing.T) {
	want := bytes.Repeat([]byte("<segment>x@y</segment>\n"), 100_000)
	rpc := &fakeRequester{resp: schema.DownloadResponse{Bytes: want}.ForWire()}
	require.Equal(t, schema.DownloadEncodingGzip, rpc.resp.Encoding, "the fixture must cross compressed")
	r := &usenetengine.Resolver{RPC: rpc}

	got, err := r.Resolve(t.Context(), "media", downloadv1alpha1.DownloadSource{
		IndexerDownload: &downloadv1alpha1.IndexerDownload{IndexerRef: "nzbgeek", GUID: "abc123"},
	})
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// An indexer's .nzb link carries its API key or passkey in the query or the
// path, and Resolve's error reaches the reconcile error, the logs and a
// Warning Event ("usenet engine: %s"). Only the host may survive in it, and a
// deadline must still read as one.
func TestResolveFetchErrorsCarryNoCredentials(t *testing.T) {
	const pathSecret, querySecret = "pathsecret-apikey", "querysecret-apikey"
	secretPath := "/getnzb/" + pathSecret + ".nzb?apikey=" + querySecret

	refused := httptest.NewServer(http.NotFoundHandler())
	refused.Close()
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer hang.Close()
	notFound := httptest.NewServer(http.NotFoundHandler())
	defer notFound.Close()
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(make([]byte, 100)) }))
	defer big.Close()

	for _, tc := range []struct {
		name     string
		base     string
		deadline bool
	}{
		{"refused", refused.URL, false},
		{"deadline", hang.URL, true},
		{"status", notFound.URL, false},
		{"tooLarge", big.URL, false},
		{"unparseable", "https://indexer.example/%zz", false},
	} {
		raw := tc.base + secretPath
		for _, branch := range []struct {
			name string
			r    *usenetengine.Resolver
			src  downloadv1alpha1.DownloadSource
		}{
			{"nzbURL", &usenetengine.Resolver{MaxBytes: 10}, downloadv1alpha1.DownloadSource{NZBURL: &raw}},
			{
				"redirectURL", &usenetengine.Resolver{MaxBytes: 10, RPC: &fakeRequester{resp: schema.DownloadResponse{RedirectURL: raw}}},
				downloadv1alpha1.DownloadSource{IndexerDownload: &downloadv1alpha1.IndexerDownload{IndexerRef: "idx", GUID: "g"}},
			},
		} {
			t.Run(tc.name+"/"+branch.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
				defer cancel()
				_, err := branch.r.Resolve(ctx, "media", branch.src)
				require.Error(t, err)
				assert.NotContains(t, err.Error(), pathSecret)
				assert.NotContains(t, err.Error(), querySecret)
				if tc.deadline {
					assert.ErrorIs(t, err, context.DeadlineExceeded)
				}
			})
		}
	}
}

// ErrUnsupportedSource used to print the whole DownloadSource with %+v: a
// tracker URL's passkey, or a magnet's announce URL carrying one.
func TestResolveUnsupportedSourceErrorCarriesNoURL(t *testing.T) {
	torrentURL := "https://tracker.example/download/abc.torrent?passkey=s3cret-passkey"
	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&tr=https%3A%2F%2Ftracker.example%2Fs3cret-passkey%2Fannounce"
	r := &usenetengine.Resolver{}
	for _, src := range []downloadv1alpha1.DownloadSource{{TorrentURL: &torrentURL}, {MagnetURL: &magnet}} {
		_, err := r.Resolve(t.Context(), "media", src)
		require.ErrorIs(t, err, usenetengine.ErrUnsupportedSource)
		assert.NotContains(t, err.Error(), "s3cret")
		assert.NotContains(t, err.Error(), "tracker.example")
	}
}

// A non-200 answer's body is drained before the connection is reused, but
// only so far: an endless error body must not hold the resolve until its
// deadline.
func TestResolveDrainsANon200BodyOnlyBoundedly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		chunk := make([]byte, 32<<10)
		for r.Context().Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	url := srv.URL
	start := time.Now()
	_, err := (&usenetengine.Resolver{}).Resolve(ctx, "media", downloadv1alpha1.DownloadSource{NZBURL: &url})
	require.Error(t, err)
	assert.NotErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second, "the drain must be bounded")
}
