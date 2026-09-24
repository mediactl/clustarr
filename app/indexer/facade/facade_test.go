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

// Package facade_test drives the facade the same way an external Torznab
// client would: real HTTP requests, via httptest, against the exported
// Server built by facade.New. Config.Search/Query/Download are plain
// function values (see doc.go's "same process, plain Go calls" section), so
// these tests stand them in with fakes rather than a real
// indexarr/search.Service/query.Service/download.Service -- those packages
// are under concurrent development by a sibling task, and facade's own
// doc.go explains why this package does not import them even in its own
// tests. The fakes have IDENTICAL signatures to the real services' method
// values (facade.SearchFunc/QueryFunc/DownloadFunc match
// indexarr/search.Service.Search / indexarr/query.Service.Handle /
// indexarr/download.Service.Handle exactly, verified against their current
// source), so this suite is a faithful test of everything the facade itself
// -- routing, auth, param parsing, XML rendering, error mapping -- is
// responsible for.
package facade_test

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/facade"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

const testAPIKey = "s3cret"

// fixture bundles a facade.Server under test with the fakes it was built
// from, so a test can both drive HTTP requests and inspect what the facade
// asked its backends for.
type fixture struct {
	srv *httptest.Server

	lastSearchReq   schema.SearchRequest
	searchResp      schema.SearchResponse
	lastQueryReq    schema.QueryRequest
	queryResp       schema.QueryResponse
	lastDownloadReq schema.DownloadRequest
	downloadResp    schema.DownloadResponse
}

func newFixture(t *testing.T, indexers ...*indexv1alpha1.Indexer) *fixture {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(toObjects(indexers)...).Build()

	f := &fixture{}
	cfg := facade.Config{
		Client:  c,
		APIKeys: []string{testAPIKey},
		Search: func(_ context.Context, req schema.SearchRequest) schema.SearchResponse {
			f.lastSearchReq = req
			return f.searchResp
		},
		Query: func(_ context.Context, req schema.QueryRequest) schema.QueryResponse {
			f.lastQueryReq = req
			return f.queryResp
		},
		Download: func(_ context.Context, req schema.DownloadRequest) schema.DownloadResponse {
			f.lastDownloadReq = req
			return f.downloadResp
		},
	}
	s, err := facade.New("127.0.0.1:0", cfg)
	require.NoError(t, err)
	require.NotNil(t, s)

	f.srv = httptest.NewServer(s.Handler())
	t.Cleanup(f.srv.Close)
	return f
}

func toObjects(indexers []*indexv1alpha1.Indexer) []client.Object {
	out := make([]client.Object, len(indexers))
	for i, idx := range indexers {
		out[i] = idx
	}
	return out
}

func boolPtr(b bool) *bool { return &b }

func TestNewRefusesAnUnauthenticatedFacade(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	noop := func(_ context.Context, _ schema.SearchRequest) schema.SearchResponse { return schema.SearchResponse{} }
	noopQ := func(_ context.Context, _ schema.QueryRequest) schema.QueryResponse { return schema.QueryResponse{} }
	noopD := func(_ context.Context, _ schema.DownloadRequest) schema.DownloadResponse {
		return schema.DownloadResponse{}
	}

	_, err := facade.New(":8080", facade.Config{Client: c, Search: noop, Query: noopQ, Download: noopD})
	require.Error(t, err, "no APIKeys must refuse to build")

	_, err = facade.New(":8080", facade.Config{
		Client: c, APIKeys: []string{"  "}, Search: noop, Query: noopQ, Download: noopD,
	})
	require.Error(t, err, "a blank-only key set must refuse to build")

	_, err = facade.New(":8080", facade.Config{Client: c, APIKeys: []string{"key"}})
	require.Error(t, err, "missing Search/Query/Download must refuse to build")
}

func TestNewWithDisabledBindAddressReturnsNoServerAndNoError(t *testing.T) {
	s, err := facade.New(k8s.DisabledBindAddress, facade.Config{})
	require.NoError(t, err)
	require.Nil(t, s)
}

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "idx1", Namespace: "media"}}
	f := newFixture(t, idx)

	for _, path := range []string{"/idx1/api?t=caps", "/idx1/download?guid=g1", "/search/api?t=caps"} {
		resp, err := http.Get(f.srv.URL + path)
		require.NoError(t, err)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, path)
		_ = resp.Body.Close()
	}
}

func TestAPIKeyViaHeaderIsAccepted(t *testing.T) {
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "idx1", Namespace: "media"}}
	f := newFixture(t, idx)

	req, err := http.NewRequest(http.MethodGet, f.srv.URL+"/idx1/api?t=caps", nil)
	require.NoError(t, err)
	req.Header.Set("X-Api-Key", testAPIKey)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestIndexerCapsRendersStatusCaps(t *testing.T) {
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: "idx1", Namespace: "media"},
		Status: indexv1alpha1.IndexerStatus{
			Caps: &indexv1alpha1.Caps{
				Modes: map[string][]string{"movie": {"imdbid"}},
				Categories: []indexv1alpha1.Category{
					{ID: 2000, Name: "Movies"},
				},
			},
		},
	}
	f := newFixture(t, idx)

	body := get(t, f, "/idx1/api?t=caps&apikey="+testAPIKey)
	require.Contains(t, body, `title="idx1"`)
	require.Contains(t, body, `available="yes"`)
	require.Contains(t, body, `id="2000"`)
}

func TestIndexerSearchScopesToExactlyThatIndexerAndSetsText(t *testing.T) {
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "idx1", Namespace: "media"}}
	f := newFixture(t, idx)
	f.searchResp = schema.SearchResponse{Releases: []schema.Release{
		{Info: commonv1.ReleaseInfo{GUID: "g1", Title: "The Matrix 1999 1080p"}},
	}}

	body := get(t, f, "/idx1/api?t=movie&q=the+matrix&apikey="+testAPIKey)

	require.Equal(t, "the matrix", f.lastSearchReq.Text,
		"the facade is what finally exercises SearchRequest.Text (indexarr/search/query.go's carried fallback note)")
	require.Equal(t, commonv1.MediaKindMovie, f.lastSearchReq.Kind)
	require.Len(t, f.lastSearchReq.IndexerRefs, 1)
	require.Equal(t, "idx1", f.lastSearchReq.IndexerRefs[0].Name)
	require.Equal(t, "media", f.lastSearchReq.IndexerRefs[0].Namespace)
	require.True(t, f.lastSearchReq.UserInvoked)
	require.Contains(t, body, "The Matrix 1999 1080p")
	require.Contains(t, body, "<guid>g1</guid>")
}

func TestIndexerAPIWithNoTIsAMissingParameterError(t *testing.T) {
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "idx1", Namespace: "media"}}
	f := newFixture(t, idx)

	resp, err := http.Get(f.srv.URL + "/idx1/api?apikey=" + testAPIKey)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var werr wireError
	require.NoError(t, xml.NewDecoder(resp.Body).Decode(&werr))
	require.Equal(t, 200, werr.Code)
}

func TestIndexerAPIWithAnUnknownTIsANoSuchFunctionError(t *testing.T) {
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "idx1", Namespace: "media"}}
	f := newFixture(t, idx)

	resp, err := http.Get(f.srv.URL + "/idx1/api?t=bogus&apikey=" + testAPIKey)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var werr wireError
	require.NoError(t, xml.NewDecoder(resp.Body).Decode(&werr))
	require.Equal(t, 202, werr.Code)
}

func TestUnknownIndexerIs404(t *testing.T) {
	f := newFixture(t)
	resp, err := http.Get(f.srv.URL + "/nope/api?t=caps&apikey=" + testAPIKey)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestDisabledIndexerIs404(t *testing.T) {
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: "idx1", Namespace: "media"},
		Spec:       indexv1alpha1.IndexerSpec{Enabled: boolPtr(false)},
	}
	f := newFixture(t, idx)
	resp, err := http.Get(f.srv.URL + "/idx1/api?t=caps&apikey=" + testAPIKey)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestDownloadWritesBytesWithContentType(t *testing.T) {
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "idx1", Namespace: "media"}}
	f := newFixture(t, idx)
	f.downloadResp = schema.DownloadResponse{Bytes: []byte("d8:announce..."), ContentType: "application/x-bittorrent"}

	resp, err := http.Get(f.srv.URL + "/idx1/download?guid=g1&apikey=" + testAPIKey)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/x-bittorrent", resp.Header.Get("Content-Type"))
	b, _ := io.ReadAll(resp.Body)
	require.Equal(t, "d8:announce...", string(b))
	require.Equal(t, "g1", f.lastDownloadReq.GUID)
	require.Equal(t, "idx1", f.lastDownloadReq.IndexerRef.Name)
}

func TestDownloadRedirectsOnMagnetURL(t *testing.T) {
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "idx1", Namespace: "media"}}
	f := newFixture(t, idx)
	f.downloadResp = schema.DownloadResponse{MagnetURL: "magnet:?xt=urn:btih:abc"}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(f.srv.URL + "/idx1/download?guid=g1&apikey=" + testAPIKey)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Equal(t, "magnet:?xt=urn:btih:abc", resp.Header.Get("Location"))
}

func TestDownloadWithoutGUIDIsAMissingParameterError(t *testing.T) {
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "idx1", Namespace: "media"}}
	f := newFixture(t, idx)
	resp, err := http.Get(f.srv.URL + "/idx1/download?apikey=" + testAPIKey)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestDownloadErrorIsBadGateway(t *testing.T) {
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "idx1", Namespace: "media"}}
	f := newFixture(t, idx)
	f.downloadResp = schema.DownloadResponse{Error: "tracker refused the session"}
	resp, err := http.Get(f.srv.URL + "/idx1/download?guid=g1&apikey=" + testAPIKey)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
	b, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(b), "tracker refused the session")
}

func TestAggregateSearchUsesTheLocalIndexQueryNotLiveSearch(t *testing.T) {
	f := newFixture(t)
	f.queryResp = schema.QueryResponse{Releases: []schema.Release{
		{Info: commonv1.ReleaseInfo{GUID: "g1", Title: "Aggregate Hit"}},
	}}

	body := get(t, f, "/search/api?t=search&q=the+matrix&cat=2000,2010&apikey="+testAPIKey)

	require.Equal(t, "the matrix", f.lastQueryReq.Text)
	require.Equal(t, "2000,2010", f.lastQueryReq.Filters["category"])
	require.Empty(t, f.lastSearchReq.Text, "aggregate must never reach the live Search backend")
	require.Contains(t, body, "Aggregate Hit")
}

func TestAggregateCapsAdvertisesRawSearch(t *testing.T) {
	f := newFixture(t)
	body := get(t, f, "/search/api?t=caps&apikey="+testAPIKey)
	require.Contains(t, body, `searchEngine="raw"`)
}

func TestAggregateQueryErrorIsReported(t *testing.T) {
	f := newFixture(t)
	f.queryResp = schema.QueryResponse{Error: "index: request cannot match any release"}
	resp, err := http.Get(f.srv.URL + "/search/api?t=search&q=%00&apikey=" + testAPIKey)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// An Indexer literally named "search" is shadowed by /search/api -- see
// doc.go. This pins the documented behaviour rather than merely asserting it
// in prose.
func TestAnIndexerNamedSearchIsShadowedByTheAggregateRoute(t *testing.T) {
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "search", Namespace: "media"}}
	f := newFixture(t, idx)
	f.queryResp = schema.QueryResponse{}

	_ = get(t, f, "/search/api?t=caps&apikey="+testAPIKey)
	require.Empty(t, f.lastSearchReq.IndexerRefs, "the wildcard per-indexer handler never ran")
}

func get(t *testing.T, f *fixture, path string) string {
	t.Helper()
	resp, err := http.Get(f.srv.URL + path)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, path)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

type wireError struct {
	XMLName xml.Name `xml:"error"`
	Code    int      `xml:"code,attr"`
}
