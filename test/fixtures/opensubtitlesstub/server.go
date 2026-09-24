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

// Package opensubtitlesstub serves a real OpenSubtitles.com REST v1 upstream
// -- a real /login, /subtitles and /download, in exactly the wire shapes
// pkg/subtitles/providers/opensubtitlescom's client sends and parses
// (verified against that package's client.go/provider.go and its own
// test/data/subtitles/opensubtitles fixtures before this file was written).
// It never reaches the Internet; the e2e cluster has no egress.
//
// # The control route
//
// Task F-7's brief is explicit that this mock must be able to return 429
// (rate limited) or 406 (quota exhausted) "on demand", so scenario 13 can
// prove the shared provider throttle (app/caption/throttle) benches this
// provider and the fetch worker falls through to the next one -- the
// behaviour most worth an e2e in the whole phase. A stub built the way
// torznabstub is (a personality baked into the URL PATH at process start)
// cannot do that without a second Deployment and a second SubtitleProvider,
// which would prove the wrong thing: two providers of the SAME type can
// never both be searched (app/caption/worker/fetch's eligible() dedups a
// provider registry by Name(), one account per provider, Bazarr's own
// model), so a scenario built that way could never show ONE provider
// getting throttled and a search falling through to a DIFFERENT one that
// still succeeds. Instead this stub keeps one small piece of mutable state,
// the current Mode, set at runtime over HTTP by POST /__mode__?mode=<...>
// and read (GET /__mode__) for diagnostics; every other route answers
// according to it. The e2e suite flips it with portForwardService, the same
// technique hack/e2e.sh already uses to reach NATS's own monitor port.
//
// /__mode__ itself always answers 200 regardless of the configured mode --
// it is also this Deployment's readiness/liveness probe target, and a probe
// that started failing the moment a scenario asked for ModeThrottled would
// flap the pod's Ready condition mid-test for a reason that has nothing to
// do with the pod's own health.
package opensubtitlesstub

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Mode selects how /login, /subtitles and /download answer.
type Mode string

// The modes this stub supports, set at runtime via POST /__mode__.
const (
	// ModeOK is the default: every route answers a healthy candidate.
	ModeOK Mode = "ok"

	// ModeThrottled makes /subtitles and /download answer HTTP 429 (Too
	// Many Requests) -- pkg/subtitles.KindTooManyRequests, the mapping
	// opensubtitlescom/client.go's statusToProviderError gives that status.
	ModeThrottled Mode = "429"

	// ModeQuotaExhausted makes /subtitles and /download answer HTTP 406
	// with the quota body OpenSubtitles.com documents -- KindDownloadLimitExceeded.
	ModeQuotaExhausted Mode = "quota"
)

// APIKey is the key every route demands via the Api-Key header, matching
// opensubtitlescom/client.go's setCommonHeadersLocked. It matches the apiKey
// entry in config/e2e/opensubtitles-stub.yaml's Secret.
const APIKey = "e2e-fixture-os-key"

// token is the fixed bearer token /login hands out and every other route
// accepts. A mock has no reason to mint a fresh one per call: the client
// caches whatever it is given (Provider.EnsureLoggedIn) and this stub only
// has to agree with itself.
const token = "e2e-fixture-os-jwt" //nolint:gosec // a fixture constant, not a credential

// FixtureLanguageTag is echoed back is not fixed -- the stub reads it from
// the request's own "languages" query parameter (buildQuery sends exactly
// one language per search) -- but FixtureReleaseInfo is: it is the
// "release" attribute of the one candidate /subtitles always answers with
// in ModeOK, exported so test/e2e/subtitle_test.go can plant a media file
// whose own filename carries the same source/resolution tokens, letting
// pkg/release corroborate the hash match the same way a real OpenSubtitles
// search result would.
const FixtureReleaseInfo = "Fixture.Subtitle.Movie.2019.1080p.WEBRip.x264-CLUSTARR"

// FixtureFileID and FixtureFileName are the one candidate's file_id/file_name
// (searchResponse.data[].attributes.files[]).
const (
	FixtureFileID   = 424242
	FixtureFileName = "fixture.srt"
)

// fixtureSRT is the subtitle body /files/fixture.srt serves: a real, valid
// SRT document (two cues), so pkg/subtitles.PostProcess's astisub.ReadFromSRT
// parses it exactly as it would parse a real download.
const fixtureSRT = "1\n00:00:01,000 --> 00:00:04,000\nHello from the OpenSubtitles.com fixture provider.\n\n" +
	"2\n00:00:05,000 --> 00:00:08,000\nThis line proves the sidecar came from opensubtitles-stub.\n"

// state is this stub's one piece of mutable, runtime-settable behaviour.
type state struct {
	mu   sync.RWMutex
	mode Mode
}

func (s *state) get() Mode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

func (s *state) set(m Mode) { s.mu.Lock(); s.mode = m; s.mu.Unlock() }

// NewHandler builds the stub. logPath is a file on the shared /data volume
// that every non-probe, non-control request is appended to, mirroring
// torznabstub.NewHandler.
func NewHandler(logPath string, logger *slog.Logger) http.Handler {
	lg := newReqLog(logPath)
	st := &state{mode: ModeOK}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /__mode__", modeGet(st))
	mux.HandleFunc("POST /__mode__", modeSet(st, logger))
	mux.HandleFunc("POST /login", login(lg, logger, st))
	mux.HandleFunc("GET /subtitles", search(lg, logger, st))
	mux.HandleFunc("POST /download", download(lg, logger, st))
	mux.HandleFunc("GET /files/"+FixtureFileName, serveFile(lg, st))
	mux.HandleFunc("/", notFound(lg, logger))
	return mux
}

func modeGet(st *state) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"mode": string(st.get())})
	}
}

// modeSet is the control route the e2e suite drives over a port-forwarded
// tunnel (see the package doc's "The control route"). An unrecognised mode
// value is refused with 400 rather than silently accepted, so a typo in a
// scenario fails at the call site instead of as a mysterious "the mock never
// throttled" fifteen minutes later.
func modeSet(st *state, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := Mode(r.URL.Query().Get("mode"))
		switch m {
		case ModeOK, ModeThrottled, ModeQuotaExhausted:
			st.set(m)
			logger.Info("opensubtitles-stub: mode changed", "mode", m)
			writeJSON(w, http.StatusOK, map[string]string{"mode": string(m)})
		default:
			logger.Warn("opensubtitles-stub: rejected an unknown mode", "mode", r.URL.Query().Get("mode"))
			http.Error(w, fmt.Sprintf("unknown mode %q, want one of %s|%s|%s", m, ModeOK, ModeThrottled, ModeQuotaExhausted),
				http.StatusBadRequest)
		}
	}
}

// loginResponse mirrors opensubtitlescom/client.go's loginResponse exactly:
// token, base_url, user.vip.
type loginResponse struct {
	Token   string `json:"token"`
	BaseURL string `json:"base_url"`
	User    struct {
		VIP bool `json:"vip"`
	} `json:"user"`
}

func login(lg *reqLog, logger *slog.Logger, st *state) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mode := string(st.get())
		if r.Header.Get("Api-Key") != APIKey {
			logger.Warn("opensubtitles-stub: bad or missing Api-Key on /login")
			lg.record(r, mode, http.StatusUnauthorized)
			http.Error(w, "invalid api key", http.StatusUnauthorized)
			return
		}
		// Login always succeeds regardless of Mode: the brief's fallthrough
		// proof wants a provider whose SEARCH is what fails (a real
		// rate-limited account can still log in), not one that never gets
		// as far as searching at all.
		lg.record(r, mode, http.StatusOK)
		writeJSON(w, http.StatusOK, loginResponse{Token: token, BaseURL: r.Host})
	}
}

// searchResponse/searchAttrs/searchFile/searchUploader mirror
// opensubtitlescom/provider.go's searchResponse exactly.
type searchFile struct {
	FileID   int    `json:"file_id"`
	FileName string `json:"file_name"`
}
type searchUploader struct {
	Name string `json:"name"`
}
type searchAttrs struct {
	Language          string         `json:"language"`
	HearingImpaired   bool           `json:"hearing_impaired"`
	ForeignPartsOnly  bool           `json:"foreign_parts_only"`
	FromTrusted       bool           `json:"from_trusted"`
	AITranslated      bool           `json:"ai_translated"`
	MachineTranslated bool           `json:"machine_translated"`
	DownloadCount     int            `json:"download_count"`
	Release           string         `json:"release"`
	MoviehashMatch    bool           `json:"moviehash_match"`
	Uploader          searchUploader `json:"uploader"`
	Files             []searchFile   `json:"files"`
}
type searchDatum struct {
	Attributes searchAttrs `json:"attributes"`
}
type searchResponse struct {
	Data []searchDatum `json:"data"`
}

// quotaBody mirrors opensubtitlescom/client.go's quotaBody: the 406 shape.
type quotaBody struct {
	Message      string `json:"message"`
	Remaining    int    `json:"remaining"`
	ResetTimeUTC string `json:"reset_time_utc"`
}

func search(lg *reqLog, logger *slog.Logger, st *state) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mode := st.get()
		if r.Header.Get("Api-Key") != APIKey || r.Header.Get("Authorization") != "Bearer "+token {
			lg.record(r, string(mode), http.StatusUnauthorized)
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}

		switch mode {
		case ModeThrottled:
			logger.Info("opensubtitles-stub: answering /subtitles 429 on purpose (ModeThrottled)")
			lg.record(r, string(mode), http.StatusTooManyRequests)
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		case ModeQuotaExhausted:
			logger.Info("opensubtitles-stub: answering /subtitles 406 on purpose (ModeQuotaExhausted)")
			lg.record(r, string(mode), http.StatusNotAcceptable)
			writeJSON(w, http.StatusNotAcceptable, quotaBody{
				Message: "Not enough download credits.", Remaining: 0,
				ResetTimeUTC: time.Now().UTC().Add(3 * time.Hour).Format(time.RFC3339),
			})
			return
		}

		lang := r.URL.Query().Get("languages")
		if lang == "" {
			lang = "en"
		}
		lg.record(r, string(mode), http.StatusOK)
		writeJSON(w, http.StatusOK, searchResponse{Data: []searchDatum{{Attributes: searchAttrs{
			Language: lang, HearingImpaired: false, ForeignPartsOnly: false, FromTrusted: true,
			AITranslated: false, MachineTranslated: false, DownloadCount: 100,
			Release: FixtureReleaseInfo, MoviehashMatch: true,
			Uploader: searchUploader{Name: "e2e-fixture"},
			Files:    []searchFile{{FileID: FixtureFileID, FileName: FixtureFileName}},
		}}}})
	}
}

// downloadRequest mirrors opensubtitlescom/provider.go's Download request
// body: {"file_id":int,"sub_format":"srt"}.
type downloadRequest struct {
	FileID    int    `json:"file_id"`
	SubFormat string `json:"sub_format"`
}

// downloadResponse mirrors opensubtitlescom/provider.go's downloadResponse:
// {"link","file_name"}.
type downloadResponse struct {
	Link     string `json:"link"`
	FileName string `json:"file_name"`
}

func download(lg *reqLog, logger *slog.Logger, st *state) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mode := st.get()
		if r.Header.Get("Api-Key") != APIKey || r.Header.Get("Authorization") != "Bearer "+token {
			lg.record(r, string(mode), http.StatusUnauthorized)
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}

		var body downloadRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			lg.record(r, string(mode), http.StatusBadRequest)
			http.Error(w, "malformed body", http.StatusBadRequest)
			return
		}
		if body.FileID != FixtureFileID {
			logger.Warn("opensubtitles-stub: /download asked for an unknown file_id", "fileID", body.FileID)
		}

		switch mode {
		case ModeThrottled:
			lg.record(r, string(mode), http.StatusTooManyRequests)
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		case ModeQuotaExhausted:
			lg.record(r, string(mode), http.StatusNotAcceptable)
			writeJSON(w, http.StatusNotAcceptable, quotaBody{
				Message: "Not enough download credits.", Remaining: 0,
				ResetTimeUTC: time.Now().UTC().Add(3 * time.Hour).Format(time.RFC3339),
			})
			return
		}

		lg.record(r, string(mode), http.StatusOK)
		writeJSON(w, http.StatusOK, downloadResponse{
			Link:     "http://" + r.Host + "/files/" + FixtureFileName,
			FileName: FixtureFileName,
		})
	}
}

// serveFile answers the short-lived download link /download hands back: the
// raw subtitle bytes, exactly as OpenSubtitles.com's own CDN link would.
func serveFile(lg *reqLog, st *state) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lg.record(r, string(st.get()), http.StatusOK)
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
		logger.Warn("opensubtitles-stub: unrecognised route", "method", r.Method, "path", r.URL.Path)
		lg.record(r, "", http.StatusNotFound)
		http.Error(w, "no fixture for this route", http.StatusNotFound)
	}
}
