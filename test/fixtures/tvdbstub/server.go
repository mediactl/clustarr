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

// Package tvdbstub serves TheTVDB v4's login/series/episodes/updates shapes
// (pkg/metadata/clients/tvdb/{tvdb,auth}.go) for one real, recorded series
// (121361, Game of Thrones) and two fixture-owned series this task adds to
// exercise daily and anime absolute numbering, which test/data/metadata/tvdb
// does not record. No credential is ever checked -- the fixture answers any
// /login body, matching "no Internet, closed network".
package tvdbstub

import (
	_ "embed"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
)

//go:embed testdata/episodes_121361_extended.json
var episodes121361 []byte

//go:embed testdata/series_900001.json
var series900001 []byte

//go:embed testdata/episodes_900001.json
var episodes900001 []byte

//go:embed testdata/series_900002.json
var series900002 []byte

//go:embed testdata/episodes_900002.json
var episodes900002 []byte

// NewHandler builds the stub. recordedDir holds the real recorded
// login.json, series_121361.json and updates_since.json, copied verbatim
// from test/data/metadata/tvdb by images/Dockerfile.e2e-fixtures.
func NewHandler(recordedDir string, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", serveFile(filepath.Join(recordedDir, "login.json"), logger))
	mux.HandleFunc("GET /updates", serveFile(filepath.Join(recordedDir, "updates_since.json"), logger))
	mux.HandleFunc("GET /series/121361/extended", serveFile(filepath.Join(recordedDir, "series_121361.json"), logger))
	// The order segment (default|official|dvd|absolute|...) is ignored: the
	// Series controller's choice of order is not this stub's to predict, so
	// it answers the same, richer episode list for any of them. Every
	// fixture episode carries both a season/number pair and an
	// absoluteNumber, so the list reads correctly under either order.
	mux.HandleFunc("GET /series/121361/episodes/{order}", serveBytes(episodes121361))
	mux.HandleFunc("GET /series/900001/extended", serveBytes(series900001))
	mux.HandleFunc("GET /series/900001/episodes/{order}", serveBytes(episodes900001))
	mux.HandleFunc("GET /series/900002/extended", serveBytes(series900002))
	mux.HandleFunc("GET /series/900002/episodes/{order}", serveBytes(episodes900002))
	mux.HandleFunc("/", notFound(logger))
	return mux
}

// serveBytes answers with fixture bytes compiled into the binary.
func serveBytes(b []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}
}

// serveFile answers with a recorded fixture read from disk at request time.
func serveFile(path string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(path)
		if err != nil {
			logger.Error("tvdbstub: recorded fixture missing", "path", path, "error", err)
			http.Error(w, "fixture not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}
}

// notFound logs every unrecognised route, so "nobody recorded that
// endpoint" shows up as a line in the stub's log rather than as a
// twenty-minute timeout in a scenario.
func notFound(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Warn("tvdbstub: unrecorded route", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery)
		http.Error(w, "no recorded fixture for this route", http.StatusNotFound)
	}
}
