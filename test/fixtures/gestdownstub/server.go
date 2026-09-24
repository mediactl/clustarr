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

// Package gestdownstub serves a real Gestdown (api.gestdown.info) upstream --
// a real show lookup and subtitle search, in exactly the wire shapes
// pkg/subtitles/providers/gestdown's client sends and parses (verified
// against that package's provider.go and its own
// test/data/subtitles/gestdown fixtures before this file was written). It
// never reaches the Internet.
//
// Unlike opensubtitlesstub, this stub has no runtime-settable failure mode:
// scenario 13's fallthrough proof needs Gestdown to be the provider that
// SUCCEEDS once OpenSubtitles.com is throttled, and Gestdown's own client
// (opensubtitlescom/gestdown's own throttle-fix, F-1's ruling R3) has no
// upstream account-wide quota concept to simulate here in the first place --
// it always answers the one canned show and subtitle any query asks for.
package gestdownstub

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
)

// FixtureShowID is the internal Gestdown show id every /shows/external/tvdb/
// lookup resolves to, regardless of the TVDB id asked for -- this stub is
// intentionally permissive; the 404/malformed-response paths are exercised
// by pkg/subtitles/providers/gestdown's own unit tests against a real
// httptest server, not duplicated here.
const FixtureShowID = "e2e00000-0000-0000-0000-0000000000gd"

// FixtureSubtitleID and FixtureVersion are the one candidate's subtitleId and
// version (release tag), mirroring gestdownstub's own test/data/gestdown
// fixtures' shape.
const (
	FixtureSubtitleID = "gd-fixture-1"
	FixtureVersion    = "CLUSTARR"
)

// fixtureDownloadURI is the downloadUri Search answers with -- a path
// relative to the endpoint, exactly as Download expects to GET it directly
// (gestdown/provider.go's own doc comment on Download).
const fixtureDownloadURI = "/subtitles/download/" + FixtureSubtitleID

// fixtureSRT is the subtitle body the download route serves: a real, valid
// SRT document, so pkg/subtitles.PostProcess's astisub.ReadFromSRT parses it
// exactly as it would parse a real download.
const fixtureSRT = "1\n00:00:02,000 --> 00:00:05,000\nHello from the Gestdown fixture provider.\n\n" +
	"2\n00:00:06,000 --> 00:00:09,000\nThis line proves the fallthrough reached Gestdown.\n"

// showsResponse mirrors gestdown/provider.go's showsResponse exactly.
type showsResponse struct {
	Shows []showEntry `json:"shows"`
}
type showEntry struct {
	ID     string `json:"id"`
	TVDbID int    `json:"tvDbId"`
}

// searchResponse mirrors gestdown/provider.go's searchResponse exactly.
type searchResponse struct {
	MatchingSubtitles []matchingSubtitle `json:"matchingSubtitles"`
}
type matchingSubtitle struct {
	SubtitleID      string `json:"subtitleId"`
	Version         string `json:"version"`
	DownloadURI     string `json:"downloadUri"`
	Completed       bool   `json:"completed"`
	HearingImpaired bool   `json:"hearingImpaired"`
}

// NewHandler builds the stub. logPath is a file on the shared /data volume
// that every non-probe request is appended to, mirroring torznabstub.NewHandler.
func NewHandler(logPath string, logger *slog.Logger) http.Handler {
	lg := newReqLog(logPath)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /shows/external/tvdb/{tvdbID}", showLookup(lg))
	mux.HandleFunc("GET /subtitles/get/{showID}/{season}/{episode}/{lang}", subtitleSearch(lg))
	mux.HandleFunc("GET "+fixtureDownloadURI, download(lg))
	mux.HandleFunc("/", notFound(lg, logger))
	return mux
}

func showLookup(lg *reqLog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Best-effort echo of the requested TVDB id into tvDbId; a
		// non-numeric path segment (which the real API would 404 on, and
		// which no scenario here ever sends) simply echoes back 0 rather
		// than failing this permissive stub.
		tvdbID, _ := strconv.Atoi(r.PathValue("tvdbID"))
		lg.record(r, http.StatusOK)
		writeJSON(w, http.StatusOK, showsResponse{Shows: []showEntry{{ID: FixtureShowID, TVDbID: tvdbID}}})
	}
}

// subtitleSearch always answers the one canned, completed candidate,
// regardless of showID/season/episode/lang: gestdown's own searchResponse
// carries no language field at all (provider.go's Search reads the
// language back off the REQUEST path it built, never off the response), so
// there is nothing this stub could usefully vary per language.
func subtitleSearch(lg *reqLog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lg.record(r, http.StatusOK)
		writeJSON(w, http.StatusOK, searchResponse{MatchingSubtitles: []matchingSubtitle{{
			SubtitleID: FixtureSubtitleID, Version: FixtureVersion, DownloadURI: fixtureDownloadURI,
			Completed: true, HearingImpaired: false,
		}}})
	}
}

func download(lg *reqLog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lg.record(r, http.StatusOK)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fixtureSRT))
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(lg *reqLog, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Warn("gestdown-stub: unrecognised route", "method", r.Method, "path", r.URL.Path)
		lg.record(r, http.StatusNotFound)
		http.Error(w, "no fixture for this route", http.StatusNotFound)
	}
}
