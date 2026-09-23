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

package cardigann

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mediactl/clustarr/pkg/torznab"
)

// This file is the seam between the engine and a caller that speaks
// Torznab: indexarr's search fan-out and RSS poll hand every indexer a
// torznab.Query, and a Cardigann-backed indexer is one more client behind
// that interface rather than a parallel search path (Phase G ruling R5).

// torznabModes maps Cardigann's mode names onto the Torznab wire values
// (pkg/torznab's SearchMode constants). The vocabularies differ for three of
// five modes; a wrong key never errors, it simply never matches a caps gate.
var torznabModes = map[string]torznab.SearchMode{
	"search":       torznab.ModeSearch,
	"tv-search":    torznab.ModeTVSearch,
	"movie-search": torznab.ModeMovieSearch,
	"music-search": torznab.ModeMusicSearch,
	"book-search":  torznab.ModeBookSearch,
}

// TorznabMode returns the Torznab wire value for a Cardigann mode name, and
// false for a name outside the schema's five.
func TorznabMode(cardigannMode string) (torznab.SearchMode, bool) {
	m, ok := torznabModes[cardigannMode]
	return m, ok
}

// cardigannMode is TorznabMode's inverse. torznab's "audio" is its spec
// alias of "music", so it folds onto music-search.
func cardigannMode(m torznab.SearchMode) string {
	switch m {
	case torznab.ModeTVSearch:
		return "tv-search"
	case torznab.ModeMovieSearch:
		return "movie-search"
	case torznab.ModeMusicSearch, torznab.ModeAudioSearch:
		return "music-search"
	case torznab.ModeBookSearch:
		return "book-search"
	default:
		return "search"
	}
}

// QueryFromTorznab converts the fan-out's query into the engine's. Limit and
// Offset have no Cardigann equivalent (a definition pages however its
// tracker pages) and are dropped.
func QueryFromTorznab(q torznab.Query) Query {
	out := Query{
		Type:       cardigannMode(q.Type),
		Q:          q.Q,
		Categories: q.Categories,
		IMDBID:     q.IMDBID,
		TMDBID:     q.TMDBID,
		TVDBID:     q.TVDBID,
		TVMazeID:   q.TVMazeID,
		Ep:         q.Episode,
		Artist:     q.Artist,
		Album:      q.Album,
		Author:     q.Author,
		Title:      q.Title,
	}
	if q.Season != nil {
		out.Season = strconv.Itoa(*q.Season)
	}
	return out
}

// RequiresSession reports whether Search and Download on d need a Session
// from Login first -- the form, post and cookie methods. A get/oneurl login
// authenticates every request from Config, and a public definition has no
// login at all.
func (d *Definition) RequiresSession() bool { return loginRequiresSession(d.Login) }

// Expired reports whether s should be replaced by a fresh Login at now. A nil
// Session is expired; a zero ExpiresAt is not (a caller-built session with no
// lifetime lasts until the tracker rejects it).
func (s *Session) Expired(now time.Time) bool {
	if s == nil {
		return true
	}
	return !s.ExpiresAt.IsZero() && !now.Before(s.ExpiresAt)
}

// CookieHeader renders s's cookies as one Cookie request-header value
// ("a=1; b=2"), the shape a plain HTTP fetcher seeds a cookie jar from.
func (s *Session) CookieHeader() string {
	if s == nil {
		return ""
	}
	parts := make([]string, 0, len(s.Cookies))
	for _, c := range s.Cookies {
		if c == nil || c.Name == "" {
			continue
		}
		parts = append(parts, (&http.Cookie{Name: c.Name, Value: c.Value}).String())
	}
	return strings.Join(parts, "; ")
}

// sessionWire is Session's persisted form. http.Cookie's own JSON shape is
// every field including Raw and Unparsed; only what attachSession sends is
// kept, so a persisted session is exactly what the next request needs.
type sessionWire struct {
	Version   int                 `json:"v"`
	Cookies   []cookieWire        `json:"cookies,omitempty"`
	Headers   map[string][]string `json:"headers,omitempty"`
	ExpiresAt time.Time           `json:"expiresAt,omitzero"`
}

type cookieWire struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// sessionWireVersion guards the persisted shape: a session written by a
// different layout is refused rather than half-decoded into a session that
// sends the wrong cookies.
const sessionWireVersion = 1

// ErrSessionFormat is returned by UnmarshalSession for bytes that are not a
// session this package wrote.
var ErrSessionFormat = errors.New("cardigann: not a persisted session")

// MarshalSession encodes s for storage (indexarr keeps it in the
// clustarr-indexer-sessions KV bucket and mirrors it into an owned Secret).
// The bytes carry credentials and must be stored as such.
func MarshalSession(s *Session) ([]byte, error) {
	if s == nil {
		return nil, errors.New("cardigann: nil session")
	}
	w := sessionWire{Version: sessionWireVersion, ExpiresAt: s.ExpiresAt.UTC()}
	for _, c := range s.Cookies {
		if c == nil || c.Name == "" {
			continue
		}
		w.Cookies = append(w.Cookies, cookieWire{Name: c.Name, Value: c.Value})
	}
	if len(s.Headers) > 0 {
		w.Headers = map[string][]string(s.Headers.Clone())
	}
	return json.Marshal(w)
}

// UnmarshalSession is MarshalSession's inverse. The error never quotes the
// input, which is a credential.
func UnmarshalSession(data []byte) (*Session, error) {
	var w sessionWire
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("%w: undecodable", ErrSessionFormat)
	}
	if w.Version != sessionWireVersion {
		return nil, fmt.Errorf("%w: version %d", ErrSessionFormat, w.Version)
	}
	s := &Session{ExpiresAt: w.ExpiresAt}
	for _, c := range w.Cookies {
		s.Cookies = append(s.Cookies, &http.Cookie{Name: c.Name, Value: c.Value})
	}
	if len(w.Headers) > 0 {
		s.Headers = http.Header(w.Headers)
	}
	return s, nil
}
