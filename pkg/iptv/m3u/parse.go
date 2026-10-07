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

// Package m3u parses an IPTV provider's M3U playlist (Live TV spec §4.1).
package m3u

import (
	"bufio"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// MaxBytes caps a playlist's size.
	MaxBytes int64 = 64 << 20
	// MaxEntries caps a playlist's channel count.
	MaxEntries = 200000
	// maxTvgKey is the longest tvg-id a "t:" key carries within the CRD's
	// 64-byte key.
	maxTvgKey = 62
)

var (
	// ErrResponseTooLarge is a playlist over MaxBytes.
	ErrResponseTooLarge = errors.New("m3u: playlist larger than 64 MiB")
	// ErrTooManyEntries is a playlist over MaxEntries.
	ErrTooManyEntries = fmt.Errorf("m3u: playlist has more than %d entries", MaxEntries)

	nameKeyEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)
)

// Entry is one playlist channel. URL and UserAgent are the stream's and
// carry credentials: they never leave the tuner's memory.
type Entry struct {
	Key       string
	Name      string
	Group     string
	TvgID     string
	TvgName   string
	Logo      string
	URL       string
	UserAgent string
}

// Playlist is a parsed M3U.
type Playlist struct {
	// GuideURLs are the header's url-tvg and x-tvg-url, in that order.
	GuideURLs []string
	Entries   []Entry
}

// NameKey is the key of an entry without a usable tvg-id: "n:" and 26
// base32 characters of a SHA-256 of its group and name. It is stable across
// the token rotations providers apply to stream URLs.
func NameKey(group, name string) string {
	sum := sha256.Sum256([]byte(group + "\x00" + name))
	return "n:" + strings.ToLower(nameKeyEncoding.EncodeToString(sum[:]))[:26]
}

// countingReader fails once more than MaxBytes have been read.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n > MaxBytes {
		return n, ErrResponseTooLarge
	}
	return n, err
}

// Parse reads an M3U playlist. It tolerates a BOM, CRLF line ends, unquoted
// attributes, commas inside quoted values, blank lines and #EXTGRP, and
// gives every entry its Key: "t:<tvg-id>" when the tvg-id is non-empty,
// unique in the playlist and short enough, else NameKey.
func Parse(r io.Reader) (Playlist, error) {
	sc := bufio.NewScanner(&countingReader{r: io.LimitReader(r, MaxBytes+1)})
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var (
		pl      Playlist
		pending *Entry
		header  bool
	)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if !header {
			line = strings.TrimPrefix(line, "\uFEFF")
			if strings.TrimSpace(line) == "" {
				continue
			}
			if !strings.HasPrefix(line, "#EXTM3U") {
				return Playlist{}, errors.New("m3u: not an M3U playlist")
			}
			header = true
			a, _ := attrs(strings.TrimPrefix(line, "#EXTM3U"))
			for _, k := range []string{"url-tvg", "x-tvg-url"} {
				if v := a[k]; v != "" {
					pl.GuideURLs = append(pl.GuideURLs, v)
				}
			}
			continue
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXTINF:"):
			a, name := attrs(strings.TrimPrefix(line, "#EXTINF:"))
			pending = &Entry{Name: name, Group: a["group-title"], TvgID: a["tvg-id"], TvgName: a["tvg-name"], Logo: a["tvg-logo"]}
		case strings.HasPrefix(line, "#EXTGRP:"):
			if pending != nil && pending.Group == "" {
				pending.Group = strings.TrimSpace(strings.TrimPrefix(line, "#EXTGRP:"))
			}
		case strings.HasPrefix(line, "#EXTVLCOPT:"):
			if v, ok := strings.CutPrefix(strings.TrimPrefix(line, "#EXTVLCOPT:"), "http-user-agent="); ok && pending != nil {
				pending.UserAgent = v
			}
		case strings.HasPrefix(line, "#"):
		default:
			if pending == nil {
				continue
			}
			if len(pl.Entries) == MaxEntries {
				return Playlist{}, ErrTooManyEntries
			}
			pending.URL = line
			pl.Entries = append(pl.Entries, *pending)
			pending = nil
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, ErrResponseTooLarge) {
			return Playlist{}, ErrResponseTooLarge
		}
		return Playlist{}, fmt.Errorf("m3u: read: %w", err)
	}
	if !header {
		return Playlist{}, errors.New("m3u: not an M3U playlist")
	}
	assignKeys(pl.Entries)
	return pl, nil
}

func assignKeys(entries []Entry) {
	seen := make(map[string]int, len(entries))
	for _, e := range entries {
		if e.TvgID != "" {
			seen[e.TvgID]++
		}
	}
	for i := range entries {
		e := &entries[i]
		if e.TvgID != "" && seen[e.TvgID] == 1 && len(e.TvgID) <= maxTvgKey {
			e.Key = "t:" + e.TvgID
		} else {
			e.Key = NameKey(e.Group, e.Name)
		}
	}
}

// attrs parses `-1 key="value" key=value,Name` (after "#EXTINF:") into its
// attributes and the name: the text after the first comma outside quotes.
func attrs(s string) (map[string]string, string) {
	out := map[string]string{}
	i := 0
	// The duration: everything up to the first space or comma.
	for i < len(s) && s[i] != ' ' && s[i] != ',' {
		i++
	}
	for i < len(s) {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] == ',' {
			return out, strings.TrimSpace(s[i+1:])
		}
		eq := strings.IndexByte(s[i:], '=')
		comma := strings.IndexByte(s[i:], ',')
		if eq < 0 || (comma >= 0 && comma < eq) {
			// A bare word, not an attribute: skip to the next space or comma.
			for i < len(s) && s[i] != ' ' && s[i] != ',' {
				i++
			}
			continue
		}
		key := strings.ToLower(strings.TrimSpace(s[i : i+eq]))
		i += eq + 1
		var val string
		if i < len(s) && s[i] == '"' {
			end := strings.IndexByte(s[i+1:], '"')
			if end < 0 {
				val, i = s[i+1:], len(s)
			} else {
				val, i = s[i+1:i+1+end], i+end+2
			}
		} else {
			j := i
			for j < len(s) && s[j] != ' ' && s[j] != ',' {
				j++
			}
			val, i = s[i:j], j
		}
		out[key] = strings.TrimSpace(val)
	}
	return out, ""
}
