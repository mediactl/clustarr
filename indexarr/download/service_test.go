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
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func fakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).Build()
}

// identityFetcher scrubs nothing; it is for the cases where the scrubbing is
// not what is under test.
type identityFetcher struct{}

func (identityFetcher) Fetch(context.Context, string) (*FetchResult, error) {
	return nil, errors.New("not used")
}
func (identityFetcher) Scrub(s string) string { return s }

// scrubbingFetcher hides one secret value, as a real indexer's fetcher does.
type scrubbingFetcher struct{ secret string }

func (scrubbingFetcher) Fetch(context.Context, string) (*FetchResult, error) {
	return nil, errors.New("not used")
}
func (f scrubbingFetcher) Scrub(s string) string { return strings.ReplaceAll(s, f.secret, "***") }

type stubFetcher struct {
	res *FetchResult
	err error
}

func (f stubFetcher) Fetch(context.Context, string) (*FetchResult, error) { return f.res, f.err }
func (stubFetcher) Scrub(s string) string                                 { return s }

func stubFetcherFor(res *FetchResult) FetcherFor {
	return func(context.Context, *indexv1alpha1.Indexer) (Fetcher, error) {
		return stubFetcher{res: res}, nil
	}
}

func nilFetcherFor(context.Context, *indexv1alpha1.Indexer) (Fetcher, error) {
	return nil, errors.New("no fetcher")
}

func TestHandleRejectsBadRequestsWithAPopulatedError(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  schema.DownloadRequest
		want string
	}{
		{"no namespace", schema.DownloadRequest{
			IndexerRef: schema.Ref{Name: "tr"}, GUID: "g", URL: "https://x/y",
		}, "namespace"},
		{"no name", schema.DownloadRequest{
			IndexerRef: schema.Ref{Namespace: "media"}, GUID: "g", URL: "https://x/y",
		}, "name"},
		{"no guid", schema.DownloadRequest{
			IndexerRef: schema.Ref{Namespace: "media", Name: "tr"}, URL: "https://x/y",
		}, "guid"},
		{"no url", schema.DownloadRequest{
			IndexerRef: schema.Ref{Namespace: "media", Name: "tr"}, GUID: "g",
		}, "download URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := (&Service{}).Handle(context.Background(), tc.req)
			require.Contains(t, got.Error, tc.want)
			require.Nil(t, got.Bytes)
			require.Empty(t, got.MagnetURL)
			require.Empty(t, got.RedirectURL)
		})
	}
}

// A well-formed request against a Service with no client or fetcher is a
// misconfiguration, and it says so rather than panicking.
func TestHandleReportsAnUnconfiguredService(t *testing.T) {
	req := schema.DownloadRequest{
		IndexerRef: schema.Ref{Namespace: "media", Name: "tr"}, GUID: "g", URL: "https://x/y",
	}
	require.Contains(t, (&Service{}).Handle(context.Background(), req).Error, "not configured")
	require.Contains(t,
		(&Service{Client: fakeClient(t)}).Handle(context.Background(), req).Error, "not configured")
}

func TestHandleResolvesTheIndexerAndRefusesADisabledOne(t *testing.T) {
	ctx := context.Background()
	ref := schema.Ref{Namespace: "media", Name: "tr"}
	req := schema.DownloadRequest{IndexerRef: ref, GUID: "g", URL: "https://tr.example/dl"}

	t.Run("missing", func(t *testing.T) {
		s := &Service{Client: fakeClient(t), Fetch: nilFetcherFor}
		got := s.Handle(ctx, req)
		require.Contains(t, got.Error, "media/tr")
	})
	t.Run("disabled", func(t *testing.T) {
		idx := testIndexer("media", "tr", "uid-3", indexv1alpha1.LimitUnitDay)
		idx.Spec.Enabled = ptr.To(false)
		s := &Service{Client: fakeClient(t, idx), Fetch: nilFetcherFor}
		got := s.Handle(ctx, req)
		require.Contains(t, got.Error, "disabled")
	})
	t.Run("in backoff is still served", func(t *testing.T) {
		idx := testIndexer("media", "tr", "uid-4", indexv1alpha1.LimitUnitDay)
		idx.Status.DisabledUntil = ptr.To(metav1.NewTime(time.Now().Add(time.Hour)))
		s := &Service{
			Client: fakeClient(t, idx),
			Fetch:  stubFetcherFor(&FetchResult{MagnetURL: "magnet:?xt=urn:btih:z"}),
		}
		got := s.Handle(ctx, req)
		require.Empty(t, got.Error,
			"escalation is health, not authorisation: an approved grab must not be stranded")
		require.Equal(t, "magnet:?xt=urn:btih:z", got.MagnetURL)
	})
	t.Run("a fetcher that will not build is a transport error", func(t *testing.T) {
		idx := testIndexer("media", "tr", "uid-7", indexv1alpha1.LimitUnitDay)
		s := &Service{Client: fakeClient(t, idx), Fetch: nilFetcherFor}
		got := s.Handle(ctx, req)
		require.Contains(t, got.Error, "build client")
	})
}

func TestClassify(t *testing.T) {
	body := func(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }
	hdr := func(ct string) http.Header { return http.Header{"Content-Type": []string{ct}} }
	final, err := url.Parse("https://tr.example/dl?passkey=s3cret")
	require.NoError(t, err)

	for _, tc := range []struct {
		name       string
		res        *FetchResult
		wantResult string
		check      func(*testing.T, schema.DownloadResponse)
	}{
		{"torrent", &FetchResult{
			Status: 200, Header: hdr("text/html"), Body: body("d8:announce1:xe"),
			ContentLen: 15, FinalURL: final,
		}, resultOK, func(t *testing.T, r schema.DownloadResponse) {
			require.Equal(t, "application/x-bittorrent", r.ContentType,
				"the bytes win over a wrong header")
			require.Equal(t, "d8:announce1:xe", string(r.Bytes))
		}},
		{"nzb", &FetchResult{
			Status: 200, Header: hdr(""), Body: body("<?xml version=\"1.0\"?><nzb/>"),
			ContentLen: 27, FinalURL: final,
		}, resultOK, func(t *testing.T, r schema.DownloadResponse) {
			require.Equal(t, "application/x-nzb", r.ContentType)
		}},
		{
			"magnet", &FetchResult{MagnetURL: "magnet:?xt=urn:btih:abc"}, resultMagnet,
			func(t *testing.T, r schema.DownloadResponse) {
				require.Equal(t, "magnet:?xt=urn:btih:abc", r.MagnetURL)
				require.Nil(t, r.Bytes)
			},
		},
		{
			"off-host", &FetchResult{OffHostURL: "https://cdn.elsewhere.invalid/x?passkey=s3cret"},
			resultRedirect, func(t *testing.T, r schema.DownloadResponse) {
				require.Equal(t, "https://cdn.elsewhere.invalid/x?passkey=s3cret", r.RedirectURL,
					"grabarr needs the URL intact; only the LOG is redacted")
			},
		},
		{"login page", &FetchResult{
			Status: 200, Header: hdr("text/html"), Body: body("<!DOCTYPE html><html>login"),
			ContentLen: 26, FinalURL: final,
		}, resultInvalidPayload, func(t *testing.T, r schema.DownloadResponse) {
			require.Contains(t, r.Error, "HTML page")
		}},
		{
			"403", &FetchResult{Status: 403, Header: hdr(""), Body: body("nope"), FinalURL: final},
			resultUnauthorized, func(t *testing.T, r schema.DownloadResponse) {
				require.Contains(t, r.Error, "403")
				require.NotContains(t, r.Error, "nope",
					"an error page's body never reaches the reply")
			},
		},
		{
			"500", &FetchResult{Status: 500, Header: hdr(""), Body: body("stack trace"), FinalURL: final},
			resultHTTPError, func(t *testing.T, r schema.DownloadResponse) {
				require.Contains(t, r.Error, "500")
				require.NotContains(t, r.Error, "stack trace")
			},
		},
		{"oversize by Content-Length", &FetchResult{
			Status: 200, Header: hdr(""), Body: body("ignored"),
			ContentLen: MaxPayloadBytes + 1, FinalURL: final,
		}, resultRedirect, func(t *testing.T, r schema.DownloadResponse) {
			require.Equal(t, final.String(), r.RedirectURL, "grabarr needs the link intact")
			require.Empty(t, r.Error)
		}},
		{"oversize with no Content-Length", &FetchResult{
			Status: 200, Header: hdr(""), Body: body(strings.Repeat("d", MaxPayloadBytes+1)),
			ContentLen: -1, FinalURL: final,
		}, resultTooLarge, func(t *testing.T, r schema.DownloadResponse) {
			require.Contains(t, r.Error, "size limit")
			require.NotContains(t, r.Error, "tr.example", "the cap error never carries a URL")
		}},
		{"empty", &FetchResult{
			Status: 200, Header: hdr(""), Body: body(""), ContentLen: 0, FinalURL: final,
		}, resultInvalidPayload, func(t *testing.T, r schema.DownloadResponse) {
			require.Contains(t, r.Error, "empty body")
		}},
		{
			"no body at all", &FetchResult{Status: 204, Header: hdr(""), FinalURL: final},
			resultInvalidPayload, func(t *testing.T, r schema.DownloadResponse) {
				require.Contains(t, r.Error, "no body")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, result := (&Service{}).classify(identityFetcher{}, tc.res, slog.Default())
			require.Equal(t, tc.wantResult, result)
			tc.check(t, got)
			require.LessOrEqual(t, len(got.Error), maxErrorChars)
			// "exactly one of Bytes, MagnetURL or RedirectURL" -- or an
			// Error and none of them.
			set := 0
			for _, on := range []bool{
				len(got.Bytes) > 0, got.MagnetURL != "", got.RedirectURL != "",
			} {
				if on {
					set++
				}
			}
			if got.Error != "" {
				require.Zero(t, set, "a failure sets no payload field")
			} else {
				require.Equal(t, 1, set, "exactly one payload field is set")
			}
		})
	}
}

// A read failure is the one classify branch whose message carries a
// third-party string verbatim, so it is the branch that proves Scrub is
// wired: the error below echoes the passkey back at us, exactly as a tracker
// that repeats the query string in its diagnostics would.
func TestErrorsNeverCarryAPasskey(t *testing.T) {
	const passkey = "deadbeefcafe1234"
	f := scrubbingFetcher{secret: passkey}
	res := &FetchResult{
		Status: 200, Header: http.Header{}, ContentLen: -1,
		Body: errReadCloser{err: errors.New("stream reset for passkey " + passkey)},
	}
	got, result := (&Service{}).classify(f, res, slog.Default())
	require.Equal(t, resultInvalidPayload, result)
	require.NotContains(t, got.Error, passkey)
	require.Contains(t, got.Error, "***")

	// And the belt: any message built by fail() with this scrubber is clean.
	msg := fail(f.Scrub, "indexarr: get https://tr.example/dl?passkey=%s: refused", passkey)
	require.NotContains(t, msg.Error, passkey)
	require.Contains(t, msg.Error, "***")
}

// errReadCloser fails on the first Read, the way a reset connection does.
type errReadCloser struct{ err error }

func (r errReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (errReadCloser) Close() error               { return nil }

// A transport failure is scrubbed too: *url.Error carries the full URL,
// passkey included, in its own Error() string.
func TestAFetchFailureIsRedactedAndScrubbed(t *testing.T) {
	const passkey = "deadbeefcafe1234"
	idx := testIndexer("media", "tr", "uid-8", indexv1alpha1.LimitUnitDay)
	s := &Service{
		Client: fakeClient(t, idx),
		Fetch: func(context.Context, *indexv1alpha1.Indexer) (Fetcher, error) {
			return scrubbingErrFetcher{secret: passkey}, nil
		},
	}
	got := s.Handle(context.Background(), schema.DownloadRequest{
		IndexerRef: schema.Ref{Namespace: "media", Name: "tr"},
		GUID:       "https://tr.example/details?passkey=" + passkey,
		URL:        "https://tr.example/dl?passkey=" + passkey,
	})
	require.NotEmpty(t, got.Error)
	require.NotContains(t, got.Error, passkey)
}

type scrubbingErrFetcher struct{ secret string }

func (f scrubbingErrFetcher) Fetch(context.Context, string) (*FetchResult, error) {
	return nil, &url.Error{
		Op:  "Get",
		URL: "https://tr.example/dl?passkey=" + f.secret,
		Err: errors.New("dial tcp: connection refused for " + f.secret),
	}
}

func (f scrubbingErrFetcher) Scrub(s string) string { return strings.ReplaceAll(s, f.secret, "***") }

// Every outcome this package reports must be in the closed vocabulary. A
// Content-Type, a tracker error string or a GUID reaching a label is
// unbounded cardinality on a Prometheus series.
func TestEveryOutcomeIsInTheClosedVocabulary(t *testing.T) {
	allowed := map[string]bool{
		resultOK: true, resultMagnet: true, resultRedirect: true, resultNoURL: true,
		resultBadRequest: true, resultNotFound: true, resultDisabled: true,
		resultUnauthorized: true, resultHTTPError: true, resultTransport: true,
		resultTooLarge: true, resultInvalidPayload: true, resultNotConfigured: true,
		resultGrabCounted: true, resultGrabDuplicate: true, resultGrabFailed: true,
	}
	require.Len(t, allowed, 16, "the vocabulary changed; update D11's list too")

	idx := testIndexer("media", "tr", "uid-9", indexv1alpha1.LimitUnitDay)
	for _, tc := range []struct {
		name  string
		s     *Service
		req   schema.DownloadRequest
		label string
	}{
		{"bad request", &Service{}, schema.DownloadRequest{}, unknownIndexerLabel},
		{"not found", &Service{Client: fakeClient(t), Fetch: nilFetcherFor}, schema.DownloadRequest{
			IndexerRef: schema.Ref{Namespace: "media", Name: "tr"}, GUID: "g", URL: "https://x/y",
		}, unknownIndexerLabel},
		{"ok", &Service{
			Client: fakeClient(t, idx),
			Fetch:  stubFetcherFor(&FetchResult{MagnetURL: "magnet:?xt=urn:btih:z"}),
		}, schema.DownloadRequest{
			IndexerRef: schema.Ref{Namespace: "media", Name: "tr"}, GUID: "g", URL: "https://x/y",
		}, "tr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, result, label := tc.s.handle(context.Background(), tc.req)
			require.True(t, allowed[result], "outcome %q is outside the closed set", result)
			require.Equal(t, tc.label, label)
		})
	}
}

// An unresolvable indexer must not put the caller's own string on a metric
// label: req.IndexerRef.Name is caller-supplied and unbounded.
func TestAnUnresolvedIndexerUsesTheConstantLabel(t *testing.T) {
	s := &Service{Client: fakeClient(t), Fetch: nilFetcherFor}
	_, _, label := s.handle(context.Background(), schema.DownloadRequest{
		IndexerRef: schema.Ref{Namespace: "media", Name: strings.Repeat("x", 400)},
		GUID:       "g",
		URL:        "https://x/y",
	})
	require.Equal(t, unknownIndexerLabel, label)
}

// A KV failure is logged and dropped: the bytes are already in the reply, and
// an accounting outage must not strand a grab that has them.
func TestAGrabCountFailureDoesNotFailTheDownload(t *testing.T) {
	idx := testIndexer("media", "tr", "uid-10", indexv1alpha1.LimitUnitDay)
	s := &Service{
		Client: fakeClient(t, idx),
		Bus:    nil, // no bus at all: accounting is skipped, not fatal
		Fetch: stubFetcherFor(&FetchResult{
			Status: 200, Header: http.Header{}, ContentLen: 5,
			Body: io.NopCloser(strings.NewReader("d1:xe")),
		}),
	}
	got := s.Handle(context.Background(), schema.DownloadRequest{
		IndexerRef: schema.Ref{Namespace: "media", Name: "tr"}, GUID: "g",
		URL: "https://tr.example/dl",
	})
	require.Empty(t, got.Error)
	require.Equal(t, "d1:xe", string(got.Bytes))
}
