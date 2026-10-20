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

// Package asssrt converts ASS and SSA subtitles to plain SubRip. Plex's web
// player can show an ASS subtitle only by burning it into the video, a
// video transcode cluster-plex never allows, while it converts a SubRip
// subtitle itself and copies the video; so every subtitle clustarr writes
// beside a file is SubRip (talos, 2026-10-20). The text survives; fonts,
// colours, positions and effects do not -- they are dropped rather than
// carried as SubRip's <font> tags, which turned black outlined sign text
// into black text with no outline.
package asssrt

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

var (
	// ErrNotASS is a script with no [Events] section.
	ErrNotASS = errors.New("asssrt: not an ASS or SSA script")
	// ErrNoCues is a script none of whose events has text left: it is never
	// written, so a sidecar is never replaced by an empty one.
	ErrNoCues = errors.New("asssrt: no event has text")
)

// Text is the plain text of an event's Text field: override blocks
// ({\pos(..)\fs20}) removed, the vector text of drawing mode ({\p1}..{\p0})
// dropped, \N and \n made line breaks and \h a space, each line trimmed
// and empty lines dropped.
func Text(s string) string {
	var b strings.Builder
	drawing := false
	for len(s) > 0 {
		if s[0] == '{' {
			if end := strings.IndexByte(s, '}'); end >= 0 {
				drawing = drawingMode(s[1:end], drawing)
				s = s[end+1:]
				continue
			}
		}
		if s[0] == '\\' && len(s) > 1 {
			switch s[1] {
			case 'N', 'n':
				if !drawing {
					b.WriteByte('\n')
				}
				s = s[2:]
				continue
			case 'h':
				if !drawing {
					b.WriteByte(' ')
				}
				s = s[2:]
				continue
			}
		}
		if !drawing {
			b.WriteByte(s[0])
		}
		s = s[1:]
	}
	var lines []string
	for _, l := range strings.Split(b.String(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

// drawingMode is the drawing state after an override block: its last \pN
// tag, N above zero drawing; a block without one leaves the state as it was.
func drawingMode(block string, cur bool) bool {
	for i := 0; ; {
		j := strings.Index(block[i:], `\p`)
		if j < 0 {
			return cur
		}
		i += j + 2
		k := i
		for k < len(block) && block[k] >= '0' && block[k] <= '9' {
			k++
		}
		if k > i { // \pos and \pbo are not the drawing tag
			n, _ := strconv.Atoi(block[i:k])
			cur = n > 0
		}
	}
}

// PacketText is the plain text of an FFmpeg ASS packet, whose data is
// "ReadOrder,Layer,Style,Name,MarginL,MarginR,MarginV,Effect,Text" with the
// timing in the packet.
func PacketText(payload string) string {
	if f := strings.SplitN(payload, ",", 9); len(f) == 9 {
		payload = f[8]
	}
	return Text(payload)
}

// cue is one SubRip cue, in milliseconds.
type cue struct {
	start, end int64
	text       string
}

// Convert turns an ASS or SSA script into plain SubRip: a cue per Dialogue
// event with text, read by the [Events] Format line's field names, in
// start order; an event repeating another's start, end and text -- a sign
// drawn in several layers -- is one cue; comments and drawings are none.
func Convert(script []byte) ([]byte, error) {
	s := strings.ReplaceAll(string(bytes.TrimPrefix(script, []byte("\ufeff"))), "\r\n", "\n")
	format := []string{"layer", "start", "end", "style", "name", "marginl", "marginr", "marginv", "effect", "text"}
	inEvents, sawEvents := false, false
	var cues []cue
	seen := map[cue]bool{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inEvents = strings.EqualFold(line, "[Events]")
			sawEvents = sawEvents || inEvents
			continue
		}
		if !inEvents {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "format":
			format = format[:0]
			for _, f := range strings.Split(value, ",") {
				format = append(format, strings.ToLower(strings.TrimSpace(f)))
			}
		case "dialogue":
			c, ok := event(format, value)
			if ok && !seen[c] {
				seen[c] = true
				cues = append(cues, c)
			}
		}
	}
	if !sawEvents {
		return nil, ErrNotASS
	}
	if len(cues) == 0 {
		return nil, ErrNoCues
	}
	slices.SortStableFunc(cues, func(a, b cue) int {
		if a.start != b.start {
			return int(a.start - b.start)
		}
		return int(a.end - b.end)
	})
	var b strings.Builder
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, Time(c.start), Time(c.end), c.text)
	}
	return []byte(b.String()), nil
}

// event reads one Dialogue line's value by the Format's fields: Text is
// always the last field and may hold commas.
func event(format []string, value string) (cue, bool) {
	f := strings.SplitN(value, ",", len(format))
	if len(f) != len(format) {
		return cue{}, false
	}
	var c cue
	var okStart, okEnd bool
	for i, name := range format {
		switch name {
		case "start":
			c.start, okStart = timestamp(f[i])
		case "end":
			c.end, okEnd = timestamp(f[i])
		}
	}
	c.text = Text(f[len(f)-1])
	return c, okStart && okEnd && c.text != "" && c.end >= c.start
}

// timestamp reads an ASS time, H:MM:SS.cc, as milliseconds.
func timestamp(s string) (int64, bool) {
	h, rest, ok1 := strings.Cut(strings.TrimSpace(s), ":")
	m, rest, ok2 := strings.Cut(rest, ":")
	sec, frac, _ := strings.Cut(rest, ".")
	if !ok1 || !ok2 {
		return 0, false
	}
	var n [3]int64
	for i, v := range []string{h, m, sec} {
		x, err := strconv.ParseInt(v, 10, 64)
		if err != nil || x < 0 {
			return 0, false
		}
		n[i] = x
	}
	ms := int64(0)
	if frac != "" {
		frac = (frac + "000")[:3]
		x, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, false
		}
		ms = x
	}
	return ((n[0]*60+n[1])*60+n[2])*1000 + ms, true
}

// Time is a SubRip timestamp, HH:MM:SS,mmm.
func Time(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, ms%1000)
}
