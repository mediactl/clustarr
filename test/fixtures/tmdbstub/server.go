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

// Package tmdbstub serves TMDB's /movie/{id} and /find/{imdb_id} shapes
// (pkg/metadata/clients/tmdb/tmdb.go, verified against tmdb_test.go) from
// the recorded JSON under test/data/metadata/tmdb/. It never talks to the
// real TMDB API -- the e2e harness runs with no Internet access at all.
//
// Note that test/data/metadata/tmdb/movie_27205.json records *Inception*,
// not Fight Club: 27205 is Inception's TMDB id and tmdb_test.go asserts
// that title. Anything planting files for this id must name them Inception.
package tmdbstub

import (
	_ "embed"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
)

//go:embed testdata/movie_900100.json
var movie900100 []byte

//go:embed testdata/movie_900101.json
var movie900101 []byte

// NewHandler builds the stub. recordedDir holds TMDB's movie/find JSON
// copied verbatim from test/data/metadata/tmdb (images/Dockerfile.e2e-fixtures
// COPYs it there at image-build time; local `go run` callers point
// --recorded-dir at ../../testdata/metadata/tmdb instead).
func NewHandler(recordedDir string, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /movie/27205", serveFile(filepath.Join(recordedDir, "movie_27205.json"), logger))
	mux.HandleFunc("GET /find/tt1375666", serveFile(filepath.Join(recordedDir, "find_imdb_tt1375666.json"), logger))
	// Any other IMDb id resolves to the recorded empty result, which is how
	// the real API reports "no such id" -- pkg/metadata maps it to
	// metadata.ErrNotFound.
	mux.HandleFunc("GET /find/{imdb}", serveFile(filepath.Join(recordedDir, "find_imdb_notfound.json"), logger))
	// Fixture-owned movies: ids no recorded fixture uses, so a scenario can
	// own a Movie outright instead of sharing Inception with every other
	// scenario that plants tmdb-27205. They are embedded rather than read
	// from recordedDir because they are this package's own data, exactly as
	// tvdbstub's fixture-owned series 900001/900002 are.
	mux.HandleFunc("GET /movie/900100", serveBytes(movie900100))
	mux.HandleFunc("GET /movie/900101", serveBytes(movie900101))
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

// serveFile returns the recorded JSON at path, or 404 when the image was
// built without it. It re-reads per request on purpose: the file is a few
// kilobytes, and a stub that picks up an edited fixture without a restart is
// easier to debug than one that caches.
func serveFile(path string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(path)
		if err != nil {
			logger.Error("tmdbstub: recorded fixture missing", "path", path, "error", err)
			http.Error(w, "fixture not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}
}

// notFound logs every unrecognised route. A stub that answers 404 silently
// turns "the controller asked for an endpoint nobody recorded" into a
// mysterious timeout twenty minutes into an e2e run.
func notFound(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Warn("tmdbstub: unrecorded route", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery)
		http.Error(w, "no recorded fixture for this route", http.StatusNotFound)
	}
}
