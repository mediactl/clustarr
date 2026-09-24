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

package torrent

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

func strPtr(s string) *string { return &s }

func TestResolveSourceMagnetURLIsReturnedDirectly(t *testing.T) {
	src := downloadv1alpha1.DownloadSource{MagnetURL: strPtr("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567")}
	got, err := resolveSource(context.Background(), nil, nil, "default", src)
	require.NoError(t, err)
	assert.Equal(t, "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567", got.Magnet)
	assert.Empty(t, got.Payload)
}

func TestResolveSourceTorrentURLFetchesTheBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fake-metainfo-bytes"))
	}))
	defer srv.Close()

	src := downloadv1alpha1.DownloadSource{TorrentURL: strPtr(srv.URL)}
	got, err := resolveSource(context.Background(), srv.Client(), nil, "default", src)
	require.NoError(t, err)
	assert.Equal(t, []byte("fake-metainfo-bytes"), got.Payload)
	assert.Empty(t, got.Magnet)
}

func TestResolveSourceTorrentURLNon200IsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	src := downloadv1alpha1.DownloadSource{TorrentURL: strPtr(srv.URL)}
	_, err := resolveSource(context.Background(), srv.Client(), nil, "default", src)
	require.Error(t, err)
}

func TestResolveSourceTorrentURLOverSizeCapIsRejected(t *testing.T) {
	huge := bytes.Repeat([]byte("a"), maxPayloadBytes+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(huge)
	}))
	defer srv.Close()

	src := downloadv1alpha1.DownloadSource{TorrentURL: strPtr(srv.URL)}
	_, err := resolveSource(context.Background(), srv.Client(), nil, "default", src)
	require.ErrorIs(t, err, ErrResponseTooLarge)
}

func TestResolveSourceIndexerDownloadBytesBranch(t *testing.T) {
	resolver := &FakeIndexerResolver{Response: schema.DownloadResponse{Bytes: []byte("indexer-bytes")}}
	src := downloadv1alpha1.DownloadSource{IndexerDownload: &downloadv1alpha1.IndexerDownload{
		IndexerRef: "myindexer", GUID: "guid-1", URL: "https://indexer.example/dl/1",
	}}
	got, err := resolveSource(context.Background(), nil, resolver, "ns-a", src)
	require.NoError(t, err)
	assert.Equal(t, []byte("indexer-bytes"), got.Payload)

	require.Len(t, resolver.Requests, 1)
	assert.Equal(t, "ns-a", resolver.Requests[0].IndexerRef.Namespace)
	assert.Equal(t, "myindexer", resolver.Requests[0].IndexerRef.Name)
	assert.Equal(t, "guid-1", resolver.Requests[0].GUID)
	assert.Equal(t, "https://indexer.example/dl/1", resolver.Requests[0].URL)
}

func TestResolveSourceIndexerDownloadMagnetBranch(t *testing.T) {
	resolver := &FakeIndexerResolver{Response: schema.DownloadResponse{MagnetURL: "magnet:?xt=urn:btih:fromindexer"}}
	src := downloadv1alpha1.DownloadSource{IndexerDownload: &downloadv1alpha1.IndexerDownload{IndexerRef: "i", GUID: "g"}}
	got, err := resolveSource(context.Background(), nil, resolver, "default", src)
	require.NoError(t, err)
	assert.Equal(t, "magnet:?xt=urn:btih:fromindexer", got.Magnet)
}

func TestResolveSourceIndexerDownloadRedirectBranchFetches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("redirected-bytes"))
	}))
	defer srv.Close()

	resolver := &FakeIndexerResolver{Response: schema.DownloadResponse{RedirectURL: srv.URL}}
	src := downloadv1alpha1.DownloadSource{IndexerDownload: &downloadv1alpha1.IndexerDownload{IndexerRef: "i", GUID: "g"}}
	got, err := resolveSource(context.Background(), srv.Client(), resolver, "default", src)
	require.NoError(t, err)
	assert.Equal(t, []byte("redirected-bytes"), got.Payload)
}

func TestResolveSourceIndexerDownloadErrorBranch(t *testing.T) {
	resolver := &FakeIndexerResolver{Response: schema.DownloadResponse{Error: "release expired"}}
	src := downloadv1alpha1.DownloadSource{IndexerDownload: &downloadv1alpha1.IndexerDownload{IndexerRef: "i", GUID: "g"}}
	_, err := resolveSource(context.Background(), nil, resolver, "default", src)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "release expired"))
}

func TestResolveSourceIndexerDownloadWithNoResolverIsAnError(t *testing.T) {
	src := downloadv1alpha1.DownloadSource{IndexerDownload: &downloadv1alpha1.IndexerDownload{IndexerRef: "i", GUID: "g"}}
	_, err := resolveSource(context.Background(), nil, nil, "default", src)
	require.Error(t, err)
}

// TestResolveSourceExpectedInfoHashCarriesThrough proves ExpectedInfoHash is
// plumbed regardless of which branch resolves the payload -- Client.Add
// (pkg/download/torrent) is the one that actually enforces the match, so
// resolveSource's only job is to not drop it.
func TestResolveSourceExpectedInfoHashCarriesThrough(t *testing.T) {
	src := downloadv1alpha1.DownloadSource{
		MagnetURL:        strPtr("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"),
		ExpectedInfoHash: strPtr("0123456789abcdef0123456789abcdef01234567"),
	}
	got, err := resolveSource(context.Background(), nil, nil, "default", src)
	require.NoError(t, err)
	assert.Equal(t, "0123456789abcdef0123456789abcdef01234567", got.ExpectedInfoHash)
}
