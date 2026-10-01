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
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// hostRecorder is a server that is not the indexer: what an attacker names
// in the facade's `url` parameter, or the cloud metadata address. It records
// every request and answers with a torrent-shaped body, so a fetch that
// should never have happened would succeed rather than fail for some
// unrelated reason.
type hostRecorder struct {
	mu      sync.Mutex
	hits    int
	cookies string
	url     string
}

func newHostRecorder(t *testing.T) *hostRecorder {
	t.Helper()
	h := &hostRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.hits++
		h.cookies += r.Header.Get("Cookie")
		h.mu.Unlock()
		_, _ = w.Write([]byte("d4:infod4:name4:fakeee"))
	}))
	t.Cleanup(srv.Close)
	// httptest listens on 127.0.0.1; "localhost" is the same listener under
	// another HOST, which is what the same-host rule judges.
	h.url = strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	return h
}

func (h *hostRecorder) seen() (int, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits, h.cookies
}

// The initial download URL is held to the rule a redirect hop is: a URL on
// another host than the indexer's is handed back unfetched, as OffHostURL,
// and nothing is sent. Before, only the redirect policy judged the host, so
// a caller-supplied `url=http://169.254.169.254/...` was fetched by indexarr
// and its body returned to the caller.
func TestFetchHandsBackAnOffHostDownloadURLUnsent(t *testing.T) {
	tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("d1:xe"))
	}))
	t.Cleanup(tracker.Close)
	evil := newHostRecorder(t)
	f := testFetcher(t, tracker.URL)

	target := evil.url + "/latest/meta-data?passkey=s3cret"
	res, err := f.Fetch(context.Background(), target)
	require.NoError(t, err)
	require.Equal(t, target, res.OffHostURL, "handed back intact, as an off-host redirect is")
	require.True(t, res.NotSent)
	require.Nil(t, res.Body)
	hits, _ := evil.seen()
	require.Zero(t, hits, "an off-host download URL must never be requested")

	// The indexer's own host is still fetched.
	res, err = f.Fetch(context.Background(), tracker.URL+"/dl")
	require.NoError(t, err)
	require.False(t, res.NotSent)
	require.NotNil(t, res.Body)
	_ = res.Body.Close()
}

// The rpc.indexarr.download verb -- and so the Torznab facade's
// /{indexer}/download, which is the same Service.Handle with the caller's
// `url` -- never fetches an off-host URL with the indexer's credentials. It
// answers with the URL itself as RedirectURL (the caller could always fetch
// it; indexarr adds nothing to it), and counts no grab, since the indexer
// was never asked.
func TestHandleNeverFetchesAnOffHostURL(t *testing.T) {
	ctx := context.Background()
	var trackerHits int
	tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trackerHits++
		_, _ = w.Write([]byte("d1:xe"))
	}))
	t.Cleanup(tracker.Close)
	evil := newHostRecorder(t)

	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tr", UID: k8stypes.UID("uid-tr")},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL:   tracker.URL,
			SecretRef: &corev1.LocalObjectReference{Name: "tr-creds"},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tr-creds"},
		Data:       map[string][]byte{"cookie": []byte("uid=trackersession")},
	}
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default()))

	s := &Service{Client: fakeClient(t, idx, secret), Bus: bus, Fetch: NewFetcherFor(fakeClient(t, idx, secret), nil)}
	target := evil.url + "/steal"
	got := s.Handle(ctx, schema.DownloadRequest{
		IndexerRef: schema.Ref{Namespace: "media", Name: "tr"}, GUID: "guid-x", URL: target,
	})
	require.Empty(t, got.Error)
	require.Empty(t, got.Bytes, "the off-host body must never reach the caller")
	require.Equal(t, target, got.RedirectURL)
	hits, cookies := evil.seen()
	require.Zero(t, hits)
	require.NotContains(t, cookies, "trackersession")
	require.Zero(t, trackerHits)

	// No grab was counted: counting the same guid now is its FIRST count.
	_, counted, err := CountGrab(ctx, bus.KV(events.BucketIndexerLimits), idx, "guid-x", time.Now())
	require.NoError(t, err)
	require.True(t, counted, "a URL indexarr never fetched is not a grab against the indexer")
}

// cardigannDownloader binds a real cardigann.Engine to one definition, as
// app/indexer/controller/indexer's client does.
type cardigannDownloader struct {
	eng cardigann.Engine
	def *cardigann.Definition
	cfg cardigann.Config
}

func (d cardigannDownloader) Download(ctx context.Context, link string) (io.ReadCloser, error) {
	return d.eng.Download(ctx, d.def, d.cfg, link)
}
func (cardigannDownloader) Secrets() []string { return nil }

// The definition-backed path, end to end through a real Engine: the
// tracker's session cookie used to be attached to any absolute URL, so
// `url=https://evil/` handed a private tracker's session to evil and its
// body to the caller. Now the engine refuses the off-site link and the
// fetcher hands it back unfetched.
func TestADefinitionBackedGrabNeverFetchesAnOffSiteURL(t *testing.T) {
	ctx := context.Background()
	tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("d1:xe"))
	}))
	t.Cleanup(tracker.Close)
	evil := newHostRecorder(t)

	def := &cardigann.Definition{Links: []string{tracker.URL + "/"}}
	cfg, err := cardigann.NewConfig(def, tracker.URL+"/", nil)
	require.NoError(t, err)
	cfg.Session = &cardigann.Session{Cookies: []*http.Cookie{{Name: "uid", Value: "trackersession"}}}
	d := cardigannDownloader{eng: cardigann.Engine{HTTP: tracker.Client()}, def: def, cfg: cfg}

	idx := definitionIndexer()
	s := &Service{
		Client: fakeClient(t, idx),
		Fetch: func(context.Context, *indexv1alpha1.Indexer) (Fetcher, error) {
			t.Fatal("a definition-backed grab went through the plain fetcher")
			return nil, nil
		},
		Definitions: func(context.Context, *indexv1alpha1.Indexer) (Fetcher, error) {
			return EngineFetcher(d), nil
		},
	}
	target := evil.url + "/steal?q=1"
	got := s.Handle(ctx, schema.DownloadRequest{
		IndexerRef: schema.Ref{Namespace: idx.Namespace, Name: idx.Name}, GUID: "g", URL: target,
	})
	require.Empty(t, got.Error)
	require.Empty(t, got.Bytes)
	require.Equal(t, target, got.RedirectURL)
	hits, cookies := evil.seen()
	require.Zero(t, hits)
	require.NotContains(t, cookies, "trackersession")

	// The site's own link still goes through the engine.
	got = s.Handle(ctx, schema.DownloadRequest{
		IndexerRef: schema.Ref{Namespace: idx.Namespace, Name: idx.Name}, GUID: "g", URL: tracker.URL + "/dl/1",
	})
	require.Empty(t, got.Error)
	require.Equal(t, "d1:xe", string(got.Bytes))
}

func TestSameOriginTreatsAnIPLiteralAsItselfOnly(t *testing.T) {
	require.True(t, sameOrigin("10.0.0.5", "10.0.0.5:8080"))
	require.False(t, sameOrigin("10.0.0.5", "0.0.5"), "an IP literal has no parent domain")
	require.False(t, sameOrigin("10.0.0.5", "x.10.0.0.5"))
	require.False(t, sameOrigin("dl.tr.example", "example"), "a single label is not the tracker's parent")
	require.True(t, sameOrigin("dl.tr.example", "tr.example"))
}
