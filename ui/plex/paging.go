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

package plex

import (
	"net/http"
	"strconv"
)

// Default paging (research §3, spec §D.2): a page size of 20 and a start of
// 0. Ruling R7 implements 0-based start -- the research note marks Plex's
// own base as unverified, and Phase H's run against a real server settles
// it; nothing here assumes 1-based.
const (
	defaultContainerStart = 0
	defaultContainerSize  = 20
)

// pageRequest is one request's paging, read from either header form (spec
// §D.2: "headers or query params").
type pageRequest struct {
	start int
	size  int
}

// parsePaging reads X-Plex-Container-Start and X-Plex-Container-Size, as a
// header first and, when absent, the query parameter of the same name
// (research §3: "also accepted as query parameters of the same name").
// Anything absent, empty or unparsable falls back to the default; a
// negative start clamps to 0 rather than reaching for a negative slice
// index.
func parsePaging(r *http.Request) pageRequest {
	start := headerOrQueryInt(r, "X-Plex-Container-Start", defaultContainerStart)
	if start < 0 {
		start = 0
	}
	size := headerOrQueryInt(r, "X-Plex-Container-Size", defaultContainerSize)
	if size <= 0 {
		size = defaultContainerSize
	}
	return pageRequest{start: start, size: size}
}

func headerOrQueryInt(r *http.Request, name string, fallback int) int {
	v := r.Header.Get(name)
	if v == "" {
		v = r.URL.Query().Get(name)
	}
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

// windowMetadata slices items to p's window, and reports the true total
// size of the whole set (before slicing) for the container's own
// totalSize. A start at or past the end of items answers an empty slice
// with the true totalSize (spec §D.2, Review Focus 3) rather than an
// out-of-range panic or a truncated total.
func windowMetadata(items []Metadata, p pageRequest) (window []Metadata, total int) {
	total = len(items)
	if p.start >= total {
		return []Metadata{}, total
	}
	end := p.start + p.size
	if end > total {
		end = total
	}
	return items[p.start:end], total
}

// setPagingHeaders sets the response paging headers research §3's [P]
// source adds beyond the body's own offset/totalSize: X-Plex-Container-Start
// (the actual offset served) and X-Plex-Container-Total-Size ("optional but
// typical").
func setPagingHeaders(w http.ResponseWriter, offset, total int) {
	w.Header().Set("X-Plex-Container-Start", strconv.Itoa(offset))
	w.Header().Set("X-Plex-Container-Total-Size", strconv.Itoa(total))
}
