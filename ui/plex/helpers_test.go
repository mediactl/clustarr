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

package plex_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/plex"
	"github.com/mediactl/clustarr/ui/projection"
)

// testScheme registers every kind the fixture builders in fixtures_test.go
// produce.
func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, catalogv1.AddToScheme(s))
	return s
}

// newTestHandler builds a plex.Handler over a fake client seeded with objs,
// wired to externalURL. h.opts.Index -> projection.BuildIndex over the fake
// client is exactly cmd/clustarr's own wiring (ui/routes.go), so a test here
// exercises the same path production does.
func newTestHandler(t *testing.T, externalURL string, objs ...client.Object) http.Handler {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	return plex.Handler(plex.Options{
		ExternalURL: externalURL,
		Index: func(ctx context.Context) (*projection.Index, error) {
			return projection.BuildIndex(ctx, c)
		},
	})
}

// newTestHandlerWithEpisodes is [newTestHandler] for a series fixture whose
// episodes are a separate slice ([fixtureSeriesAndEpisodes]'s own shape).
func newTestHandlerWithEpisodes(t *testing.T, externalURL string, series *catalogv1.Series, episodes []*catalogv1.Episode) http.Handler {
	t.Helper()
	objs := make([]client.Object, 0, len(episodes)+1)
	objs = append(objs, series)
	for _, e := range episodes {
		objs = append(objs, e)
	}
	return newTestHandler(t, externalURL, objs...)
}

// doRequest sends req through h and returns the recorded response.
func doRequest(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// getJSON issues GET path (with optional header pairs) and returns the
// response.
func getJSON(t *testing.T, h http.Handler, path string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	return doRequest(h, req)
}

// postJSON issues POST path with body marshalled as JSON.
func postJSON(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	return doRequest(h, req)
}

// requireGolden compares rec's JSON body against testdata/<name>.json,
// through require.JSONEq -- a semantic, key-order-independent comparison,
// which is what "encoding/json-canonicalised" (the task brief's own words)
// means in practice: both sides are parsed back to the same in-memory form
// before comparison, so authoring the golden file need not match Go's own
// struct field order.
// decodeJSON is a thin json.Unmarshal wrapper, for the tests that assert on
// a few fields of a response rather than comparing it whole against a
// golden.
func decodeJSON(body []byte, v any) error {
	return json.Unmarshal(body, v)
}

func requireGolden(t *testing.T, rec *httptest.ResponseRecorder, name string) {
	t.Helper()
	golden, err := os.ReadFile("testdata/" + name + ".json")
	require.NoError(t, err, "read testdata/%s.json", name)
	require.JSONEq(t, string(golden), rec.Body.String(), "response for %s did not match its golden", name)
}
