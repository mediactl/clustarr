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

// Package xmltv parses XMLTV guides and builds the XEPG guide a provider
// serves Plex (Live TV spec §4.3).
package xmltv

import (
	"bufio"
	"compress/gzip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxBytes caps a guide's uncompressed size.
const MaxBytes int64 = 1 << 30

// maxBytes is MaxBytes, lowered by tests (export_test.go).
var maxBytes = MaxBytes

// ErrResponseTooLarge is a guide over MaxBytes uncompressed.
var ErrResponseTooLarge = errors.New("xmltv: guide larger than 1 GiB uncompressed")

// timeLayout is XMLTV's time format; timeLayoutBare its zone-less form,
// read as UTC.
const (
	timeLayout     = "20060102150405 -0700"
	timeLayoutBare = "20060102150405"
)

// Channel is one XMLTV <channel>.
type Channel struct {
	ID           string
	DisplayNames []string
	Icon         string
}

// Programme is one XMLTV <programme>. Inner is the element's inner XML,
// kept verbatim, so the built guide carries every field the source had.
type Programme struct {
	Channel     string
	Start, Stop time.Time
	Inner       []byte
}

// Guide is a parsed XMLTV guide.
type Guide struct {
	Channels   []Channel
	Programmes map[string][]Programme
}

type cappedReader struct {
	r io.Reader
	n int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n > maxBytes {
		return n, ErrResponseTooLarge
	}
	return n, err
}

// Parse reads an XMLTV guide, plain or gzip (detected by its magic bytes),
// streaming, never building a DOM. It returns every channel and, for the
// channel ids want names, the programmes overlapping [from, to). A nil want
// parses channels only.
func Parse(r io.Reader, want map[string]bool, from, to time.Time) (Guide, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	var src io.Reader = br
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return Guide{}, fmt.Errorf("xmltv: gzip: %w", err)
		}
		defer func() { _ = zr.Close() }()
		src = zr
	}
	d := xml.NewDecoder(&cappedReader{r: io.LimitReader(src, maxBytes+1)})
	d.Strict = false
	d.CharsetReader = charsetReader
	g := Guide{Programmes: map[string][]Programme{}}
	sawTV := false
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if errors.Is(err, ErrResponseTooLarge) {
				return Guide{}, ErrResponseTooLarge
			}
			return Guide{}, fmt.Errorf("xmltv: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "tv":
			sawTV = true
		case "channel":
			var c struct {
				ID           string   `xml:"id,attr"`
				DisplayNames []string `xml:"display-name"`
				Icons        []struct {
					Src string `xml:"src,attr"`
				} `xml:"icon"`
			}
			if err := d.DecodeElement(&c, &se); err != nil {
				return Guide{}, fmt.Errorf("xmltv: channel: %w", err)
			}
			ch := Channel{ID: c.ID}
			for _, n := range c.DisplayNames {
				if n = strings.TrimSpace(n); n != "" {
					ch.DisplayNames = append(ch.DisplayNames, n)
				}
			}
			if len(c.Icons) > 0 {
				ch.Icon = c.Icons[0].Src
			}
			g.Channels = append(g.Channels, ch)
		case "programme":
			channel := attr(se, "channel")
			if !want[channel] {
				if err := d.Skip(); err != nil {
					return Guide{}, fmt.Errorf("xmltv: programme: %w", err)
				}
				continue
			}
			var p struct {
				Inner []byte `xml:",innerxml"`
			}
			if err := d.DecodeElement(&p, &se); err != nil {
				return Guide{}, fmt.Errorf("xmltv: programme: %w", err)
			}
			start, err1 := parseTime(attr(se, "start"))
			stop, err2 := parseTime(attr(se, "stop"))
			if err1 != nil || err2 != nil || !stop.After(from) || !start.Before(to) {
				continue
			}
			g.Programmes[channel] = append(g.Programmes[channel], Programme{Channel: channel, Start: start, Stop: stop, Inner: p.Inner})
		}
	}
	if !sawTV {
		return Guide{}, errors.New("xmltv: no <tv> element")
	}
	return g, nil
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(timeLayout, s); err == nil {
		return t, nil
	}
	return time.ParseInLocation(timeLayoutBare, s, time.UTC)
}

// charsetReader passes ISO-8859-1 through as UTF-8; encoding/xml handles
// UTF-8 itself. Any other declared charset is refused.
func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "iso-8859-1", "latin1", "latin-1", "iso8859-1":
		return &latin1Reader{r: bufio.NewReader(input)}, nil
	case "us-ascii", "ascii":
		return input, nil
	}
	return nil, fmt.Errorf("xmltv: unsupported charset %q", charset)
}

type latin1Reader struct {
	r   *bufio.Reader
	buf []byte
}

func (l *latin1Reader) Read(p []byte) (int, error) {
	for len(l.buf) < len(p) {
		b, err := l.r.ReadByte()
		if err != nil {
			if len(l.buf) == 0 {
				return 0, err
			}
			break
		}
		l.buf = utf8.AppendRune(l.buf, rune(b))
	}
	n := copy(p, l.buf)
	l.buf = l.buf[n:]
	return n, nil
}
