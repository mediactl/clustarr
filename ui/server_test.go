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

package ui_test

import (
	"bufio"
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui"
)

// readSSEEvent reads one complete SSE event -- every line up to and
// including the blank line that terminates it -- and returns it joined back
// together, newlines included, exactly as it arrived on the wire. It is
// TestPipelineEventsStreamsServerSentEvents' own inline read loop, factored
// out so the Task D3-3 tests below can read more than one event off the
// same connection.
func readSSEEvent(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	var lines []string
	for {
		line, err := r.ReadString('\n')
		lines = append(lines, line)
		if err != nil {
			require.NoError(t, err, "reading SSE event")
		}
		if strings.TrimSpace(line) == "" && len(lines) > 1 {
			break
		}
	}
	return strings.Join(lines, "")
}

func TestHealthzDoesNotDependOnTheCluster(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, rec.Code)
}

// TestNewServerWarnsThroughTheContextLogger covers the removal of
// ui.Server's *slog.Logger field (CLAUDE.md: "no logger struct fields").
//
// The auth warning is the one line this service logs outside a request, so
// it is the one that would have justified keeping a field. It does not:
// NewServer takes a ctx for it and keeps nothing. Everything else logs
// through logging.FromContext(r.Context()), which ui.Run installs via the
// http.Server's BaseContext and which degrades to the discard logger under
// httptest -- the designed behaviour, and why the tests above can call
// NewServer with a bare context and see no output.
func TestNewServerWarnsThroughTheContextLogger(t *testing.T) {
	var buf bytes.Buffer
	ctx := logging.NewContext(t.Context(), slog.New(slog.NewJSONHandler(&buf, nil)))

	ui.NewServer(ctx, ui.Options{})

	require.Contains(t, buf.String(), "ingress authentication",
		"NewServer must log the §A3.5 warning through the logger on its context")
}
