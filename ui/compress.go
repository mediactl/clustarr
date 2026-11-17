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

package ui

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
)

// compress gzips what the mux answers (2026-11-17): a season's rows were
// 2 MB on the wire for 38 KB compressed, the Live TV pages 14 and 22 MB,
// every 10 s poll its whole fragment. It compresses when the request
// accepts gzip and asks for no range, the path is not a stream, an image
// or Plex's (SSE must flush as it goes, art is already compressed, PMS is
// not a browser), and the response, at its first write, carries no
// encoding of its own (compressed() and the art handler set theirs), a
// body-bearing status and a text content type.
func compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r) || r.Header.Get("Range") != "" || uncompressedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressWriter{ResponseWriter: w}
		defer cw.close()
		next.ServeHTTP(cw, r)
	})
}

func uncompressedPath(p string) bool {
	for _, prefix := range []string{"/events/", "/art/", "/plex/", "/livetv/logo/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

var compressibleTypes = []string{
	"text/html", "text/css", "text/plain", "text/javascript",
	"application/javascript", "application/json", "image/svg+xml",
}

// gzipWriters is a free list of gzip writers (a channel, not a sync.Pool:
// TestUINeverWrites reads any .Put( selector as a Kubernetes write).
var gzipWriters = make(chan *gzip.Writer, 16)

func takeGzip(w io.Writer) *gzip.Writer {
	select {
	case gz := <-gzipWriters:
		gz.Reset(w)
		return gz
	default:
		gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		return gz
	}
}

func giveGzip(gz *gzip.Writer) {
	select {
	case gzipWriters <- gz:
	default:
	}
}

// compressWriter decides at the first WriteHeader or Write whether the
// response is compressed, then writes through a pooled gzip writer.
type compressWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
}

func (c *compressWriter) decide(status int) {
	if c.decided {
		return
	}
	c.decided = true
	h := c.Header()
	if h.Get("Content-Encoding") != "" || status == http.StatusNoContent || status == http.StatusNotModified || status < 200 {
		return
	}
	ct := h.Get("Content-Type")
	ok := false
	for _, t := range compressibleTypes {
		if strings.HasPrefix(ct, t) {
			ok = true
			break
		}
	}
	if !ok {
		return
	}
	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")
	h.Del("Content-Length")
	c.gz = takeGzip(c.ResponseWriter)
}

func (c *compressWriter) WriteHeader(status int) {
	c.decide(status)
	c.ResponseWriter.WriteHeader(status)
}

func (c *compressWriter) Write(b []byte) (int, error) {
	if !c.decided {
		// net/http would sniff the type at the first write; do the same so
		// a handler that set none is still judged.
		if c.Header().Get("Content-Type") == "" {
			c.Header().Set("Content-Type", http.DetectContentType(b))
		}
		c.decide(http.StatusOK)
	}
	if c.gz != nil {
		return c.gz.Write(b)
	}
	return c.ResponseWriter.Write(b)
}

// Flush sends what has been written so far, for a handler that streams.
func (c *compressWriter) Flush() {
	if c.gz != nil {
		_ = c.gz.Flush()
	}
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (c *compressWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *compressWriter) close() {
	if c.gz == nil {
		return
	}
	_ = c.gz.Close()
	giveGzip(c.gz)
	c.gz = nil
}
