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

// Package torznabstub serves a real Torznab upstream -- a real t=caps
// document and real <rss><channel><item> result feeds, byte for byte the
// XML in testdata/, parsed by pkg/torznab's own ParseCaps/ParseResults.
// It never reaches the Internet; the e2e cluster has no egress.
//
// Three personalities, selected by path, so one Deployment covers every
// shape scenario 17 needs:
//
//	/api            the healthy indexer. Requires apikey; dispatches on t.
//	/api/searchdown t=caps succeeds, every SEARCH fails with HTTP 500.
//	/api/down       always HTTP 500, including t=caps.
//
// /api is the DEFAULT Indexer.spec.generic.apiPath, so the happy path
// exercises the default; the other two are reachable only if indexarr
// actually joins apiPath onto baseURL, which is what makes the e2e suite's
// assertion on the logged path meaningful.
//
// # Why /api/searchdown exists, and why the escalation leg uses it
//
// The brief specified only /api/down for the backoff leg. Against the real
// tree that personality cannot drive the escalation ladder at all, and the
// reason is structural rather than a timing accident:
//
//   - indexarr/status.RecordFailure is called where a failure is OBSERVED --
//     the RSS poll and the search fan-out -- never by the Indexer
//     reconciler (rulings R6/R15);
//   - the reconciler seeds the RSS poll chain only for a HEALTHY Indexer
//     (indexarr/controller/indexer's seedRSSSchedule), and "healthy" here
//     requires a successful caps probe;
//   - the search fan-out skips an Indexer whose status.caps is nil, with
//     "caps not probed" (indexarr/search/select.go).
//
// So an Indexer whose caps NEVER probe is never polled and never queried:
// nothing calls RecordFailure, status.escalationLevel stays 0 and
// status.disabledUntil stays nil forever. An indexer that answers caps and
// then fails its searches is both the realistic failure mode (a live
// indexer whose search endpoint breaks) and the only one that exercises the
// ladder. /api/down is kept because "an indexer that never probes caps is
// NOT Ready and does NOT escalate" is itself worth asserting, and asserting
// it is what stops the next reader from re-adopting it for the ladder.
package torznabstub

import (
	_ "embed"
	"log/slog"
	"net/http"
)

//go:embed testdata/caps.xml
var capsXML []byte

//go:embed testdata/movie_900100.xml
var movie900100XML []byte

//go:embed testdata/rss.xml
var rssXML []byte

//go:embed testdata/empty.xml
var emptyXML []byte

//go:embed testdata/error_100.xml
var error100XML []byte

// APIKey is the key every personality demands. It matches the apikey entry
// in config/e2e/torznab-stub.yaml's Secret; an Indexer whose secretRef never
// reached the client gets Newznab error 100, not results, so credential
// plumbing cannot pass by being ignored.
const APIKey = "e2e-fixture-key"

// Paths the handler serves. They are exported so config/e2e and the e2e
// suite name the same strings this file routes on.
const (
	// PathHealthy is Indexer.spec.generic.apiPath's own CRD default.
	PathHealthy = "/api"

	// PathSearchDown answers t=caps and fails every search.
	PathSearchDown = "/api/searchdown"

	// PathDown fails everything, caps included.
	PathDown = "/api/down"
)

// NewHandler builds the stub. logPath is a file on the shared /data volume
// that every non-probe request is appended to.
func NewHandler(logPath string, logger *slog.Logger) http.Handler {
	lg := newReqLog(logPath)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+PathHealthy, api(lg, logger, true))
	mux.HandleFunc("GET "+PathSearchDown, api(lg, logger, false))
	mux.HandleFunc("GET "+PathDown, down(lg, logger))
	mux.HandleFunc("/", notFound(lg, logger))
	return mux
}

// api is the caps-answering personality. searchOK false makes every
// function other than t=caps fail with HTTP 500, which is what drives
// indexarr's RecordFailure path from a real HTTP failure rather than a
// simulated one.
func api(lg *reqLog, logger *slog.Logger, searchOK bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("apikey") != APIKey {
			logger.Warn("torznabstub: bad or missing apikey", "path", r.URL.Path, "query", r.URL.RawQuery)
			// Newznab answers a credential failure with HTTP 200 and an
			// <error> element (docs/research/indexers.md §4.5), which is
			// what pkg/torznab's readOrError looks for. Serving a bare 401
			// would exercise a path real indexers do not use.
			write(w, lg, r, http.StatusOK, error100XML)
			return
		}
		if q.Get("t") == "caps" {
			write(w, lg, r, http.StatusOK, capsXML)
			return
		}
		if !searchOK {
			logger.Info("torznabstub: failing this search on purpose",
				"path", r.URL.Path, "query", r.URL.RawQuery)
			fail(w, lg, r)
			return
		}
		switch q.Get("t") {
		case "movie":
			if q.Get("tmdbid") == "900100" || q.Get("imdbid") == "9000100" {
				write(w, lg, r, http.StatusOK, movie900100XML)
				return
			}
			// A real indexer answers an unknown id with an empty feed, not
			// an error, and pkg/torznab must parse that as zero releases.
			write(w, lg, r, http.StatusOK, emptyXML)
		case "search":
			// t=search with no q is the RSS poll.
			write(w, lg, r, http.StatusOK, rssXML)
		case "tvsearch":
			write(w, lg, r, http.StatusOK, emptyXML)
		default:
			logger.Warn("torznabstub: unsupported function", "t", q.Get("t"), "query", r.URL.RawQuery)
			write(w, lg, r, http.StatusOK, error100XML)
		}
	}
}

// down always fails, caps included.
func down(lg *reqLog, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Info("torznabstub: failing on purpose", "path", r.URL.Path, "query", r.URL.RawQuery)
		fail(w, lg, r)
	}
}

// fail writes the dead-indexer response. http.Error writes text/plain, so
// pkg/torznab's readOrError sees a non-2xx with no <error> body and returns
// "torznab: unexpected status 500" -- exactly what a real dead indexer
// produces.
func fail(w http.ResponseWriter, lg *reqLog, r *http.Request) {
	lg.record(r, http.StatusInternalServerError)
	http.Error(w, "fixture indexer is down on purpose", http.StatusInternalServerError)
}

func write(w http.ResponseWriter, lg *reqLog, r *http.Request, status int, body []byte) {
	lg.record(r, status)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// notFound logs every unrecognised route, for the same reason tmdbstub and
// tvdbstub do: "nobody serves that endpoint" must show up as a line in the
// stub's log, not as a twenty-minute timeout in a scenario.
func notFound(lg *reqLog, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Warn("torznabstub: unrecognised route", "method", r.Method,
			"path", r.URL.Path, "query", r.URL.RawQuery)
		lg.record(r, http.StatusNotFound)
		http.Error(w, "no fixture for this route", http.StatusNotFound)
	}
}
