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

// Package trailers is the record of an item's own trailer: the video a
// metadata provider names for a Movie or Series Plex's metadata service
// has no trailer for, fetched onto the media volume by the trailers agent
// and answered to Plex by the provider's extras route as a clip whose part
// is that file (talos, 2026-11-17: Plex held no trailer for 203 of 1,273
// movies and 390 of 541 shows).
//
// One entry per item lives in the clustarr-trailers KV bucket, keyed by the
// item's kind and uid. The trailers agent is the bucket's only writer; the
// ui reads it. The package links no Kubernetes client.
package trailers

import (
	"cmp"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// State is where an item's trailer stands.
type State string

const (
	// StateReady: the file at Entry.Path is the item's trailer.
	StateReady State = "ready"
	// StatePlex: Plex's metadata service holds a trailer for the item, so
	// none is fetched.
	StatePlex State = "plex"
	// StateNone: no provider names a video for the item.
	StateNone State = "none"
	// StateFailed: the fetch failed; it is tried again at Entry.RetryAt.
	StateFailed State = "failed"
)

// Video is one video a metadata provider names for an item.
type Video struct {
	// Site is where it is hosted: "YouTube" or "Vimeo".
	Site string `json:"site"`
	// Key is the site's id of the video.
	Key string `json:"key"`
	// Name is the video's title at the provider.
	Name string `json:"name,omitempty"`
	// Type is the provider's kind: "Trailer", "Teaser", and others that
	// are never picked.
	Type string `json:"type,omitempty"`
	// Official is whether the provider marks it the rights holder's.
	Official bool `json:"official,omitempty"`
	// Language is its ISO 639-1 language, "" when the provider gives none.
	Language string `json:"language,omitempty"`
	// Height is the best resolution the provider lists, 0 when unknown.
	Height int `json:"height,omitempty"`
	// PublishedAt is when it was published, zero when unknown.
	PublishedAt time.Time `json:"publishedAt,omitzero"`
	// Provider is the metadata provider that named it.
	Provider string `json:"provider,omitempty"`
}

// Entry is one item's trailer record.
type Entry struct {
	State State `json:"state"`
	// CheckedAt is when the state was decided.
	CheckedAt time.Time `json:"checkedAt"`
	// Video is the video picked, for a ready or failed entry.
	Video *Video `json:"video,omitempty"`

	// Path is the trailer file as clustarr mounts the media volume; the
	// rest are what the file holds. Set for a ready entry.
	Path           string `json:"path,omitempty"`
	SizeBytes      int64  `json:"sizeBytes,omitempty"`
	DurationMillis int64  `json:"durationMillis,omitempty"`
	Width          int    `json:"width,omitempty"`
	Height         int    `json:"height,omitempty"`
	VideoCodec     string `json:"videoCodec,omitempty"`
	AudioCodec     string `json:"audioCodec,omitempty"`

	// Attempts, Error and RetryAt are a failed entry's.
	Attempts int       `json:"attempts,omitempty"`
	Error    string    `json:"error,omitempty"`
	RetryAt  time.Time `json:"retryAt,omitzero"`
}

// KindMovie and KindSeries are the kinds an entry is kept for.
const (
	KindMovie  = "movie"
	KindSeries = "series"
)

// Key is an item's key in the bucket: its kind and its uid, both of which
// are made of characters a KV key takes as they are.
func Key(kind, uid string) string {
	return kind + "." + uid
}

// Encode serialises an entry.
func Encode(e Entry) ([]byte, error) { return json.Marshal(e) }

// Decode parses an entry.
func Decode(b []byte) (Entry, error) {
	var e Entry
	err := json.Unmarshal(b, &e)
	return e, err
}

// Dir is where a root folder's trailers are kept: a directory no library
// walk enters.
func Dir(rootFolder string) string {
	return filepath.Join(rootFolder, ".clustarr", "trailers")
}

// File is an item's trailer file under its root folder.
func File(rootFolder, kind, uid string) string {
	return filepath.Join(Dir(rootFolder), kind+"-"+uid+".mp4")
}

// How long a decision stands before the item is looked at again, and how
// a failed fetch backs off.
const (
	// PlexRecheck: Plex's service may drop a trailer it held.
	PlexRecheck = 30 * 24 * time.Hour
	// NoneRecheck: a provider gains a trailer for a new title.
	NoneRecheck = 14 * 24 * time.Hour
	// MaxAttempts is when a failed fetch stops being tried until
	// NoneRecheck has passed.
	MaxAttempts = 5
	firstRetry  = time.Hour
	maxRetry    = 7 * 24 * time.Hour
)

// RetryAfter is how long after its attempts-th failure a fetch is tried
// again: an hour, then four times as long each time, a week at most.
func RetryAfter(attempts int) time.Duration {
	d := firstRetry
	for i := 1; i < attempts; i++ {
		if d *= 4; d >= maxRetry {
			return maxRetry
		}
	}
	return d
}

// Due is whether an entry's decision has lapsed at now and the item is to
// be looked at again. A ready entry never lapses: its file is checked
// where it lies.
func (e Entry) Due(now time.Time) bool {
	switch e.State {
	case StateReady:
		return false
	case StatePlex:
		return now.Sub(e.CheckedAt) >= PlexRecheck
	case StateNone:
		return now.Sub(e.CheckedAt) >= NoneRecheck
	case StateFailed:
		if e.Attempts >= MaxAttempts {
			return now.Sub(e.CheckedAt) >= NoneRecheck
		}
		return !now.Before(e.RetryAt)
	}
	return true
}

// keyPattern is a video id a site URL is built from: nothing that could be
// read as an option or a path.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{4,64}$`)

// URL is the video's page at its site, "" for a site that is not fetched
// from or a key that is not an id.
func (v Video) URL() string {
	if !keyPattern.MatchString(v.Key) || strings.HasPrefix(v.Key, "-") {
		return ""
	}
	switch strings.ToLower(v.Site) {
	case "youtube":
		return "https://www.youtube.com/watch?v=" + v.Key
	case "vimeo":
		return "https://vimeo.com/" + v.Key
	}
	return ""
}

// Pick is the video an item's trailer is fetched from: among those with a
// page to fetch, a trailer before a teaser and nothing else; then the
// rights holder's own; then one in language, then English, then any; then
// the sharpest; then the newest, and the provider's order after that.
// False when none qualifies.
func Pick(videos []Video, language string) (Video, bool) {
	language = strings.ToLower(language)
	rank := func(v Video) (typ, official, lang int) {
		switch strings.ToLower(v.Type) {
		case "trailer":
			typ = 0
		case "teaser":
			typ = 1
		default:
			typ = 2
		}
		if !v.Official {
			official = 1
		}
		switch l := strings.ToLower(v.Language); {
		case language != "" && l == language:
			lang = 0
		case l == "en":
			lang = 1
		default:
			lang = 2
		}
		return typ, official, lang
	}
	var pool []Video
	for _, v := range videos {
		if t, _, _ := rank(v); t < 2 && v.URL() != "" {
			pool = append(pool, v)
		}
	}
	if len(pool) == 0 {
		return Video{}, false
	}
	slices.SortStableFunc(pool, func(a, b Video) int {
		at, ao, al := rank(a)
		bt, bo, bl := rank(b)
		return cmp.Or(
			cmp.Compare(at, bt), cmp.Compare(ao, bo), cmp.Compare(al, bl),
			cmp.Compare(b.Height, a.Height), b.PublishedAt.Compare(a.PublishedAt),
		)
	})
	return pool[0], true
}

// PlexTrailerType is the extraType Plex gives a trailer.
const PlexTrailerType = 1

// IsPlexTrailer is whether one of Plex's extras, as Plex's metadata
// service sends it, is a trailer.
func IsPlexTrailer(extra json.RawMessage) bool {
	var v struct {
		ExtraType int    `json:"extraType"`
		Subtype   string `json:"subtype"`
	}
	return json.Unmarshal(extra, &v) == nil && (v.ExtraType == PlexTrailerType || strings.EqualFold(v.Subtype, "trailer"))
}

// PathMapping is one directory of the media volume as clustarr mounts it
// and as Plex does.
type PathMapping struct {
	Clustarr, Plex string
}

// ParsePathMapping reads "<clustarr path>=<plex path>", both absolute.
func ParsePathMapping(s string) (PathMapping, error) {
	c, p, ok := strings.Cut(s, "=")
	c, p = filepath.Clean(strings.TrimSpace(c)), filepath.Clean(strings.TrimSpace(p))
	if !ok || !filepath.IsAbs(c) || !filepath.IsAbs(p) {
		return PathMapping{}, fmt.Errorf("a path mapping is \"<clustarr path>=<plex path>\", both absolute, not %q", s)
	}
	return PathMapping{Clustarr: c, Plex: p}, nil
}

// PlexPath is path as Plex mounts it: under the longest mapping that
// holds it. False when no mapping does, and Plex cannot be told where the
// file is.
func PlexPath(mappings []PathMapping, path string) (string, bool) {
	path = filepath.Clean(path)
	best := -1
	for i, m := range mappings {
		if path != m.Clustarr && !strings.HasPrefix(path, strings.TrimSuffix(m.Clustarr, "/")+"/") {
			continue
		}
		if best < 0 || len(m.Clustarr) > len(mappings[best].Clustarr) {
			best = i
		}
	}
	if best < 0 {
		return "", false
	}
	m := mappings[best]
	return filepath.Join(m.Plex, strings.TrimPrefix(path, m.Clustarr)), true
}
