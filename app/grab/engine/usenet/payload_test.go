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
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	usenetengine "github.com/mediactl/clustarr/app/grab/engine/usenet"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// fakeRequester is a minimal events.Requester test double, the same shape
// catalogarr/worker/search.FakeSearchRPC uses for events.RPCIndexSearch.
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
