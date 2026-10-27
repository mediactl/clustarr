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

package relay

import "encoding/binary"

// Language is the language a stream's program map is made to declare for
// its audio and subtitle tracks (owner, 2026-10-27: Plex shows a track whose
// ISO 639 descriptor is missing or "und" as Unknown; tvload.win's ESPN said
// und, its Weather Channel eng; the same language names the subtitles).
type Language struct {
	// Code is the ISO 639-2 three-letter code; empty rewrites nothing.
	Code string
	// Force replaces a declared language too, not only a missing or
	// undetermined one: a channel's own setting over the stream's word.
	Force bool
}

// Fill is a provider's default language: declared where the stream names
// none or an undetermined one.
func Fill(code string) Language { return Language{Code: code} }

// tsRewriter rewrites a connection's PMT packets so every audio and
// subtitle stream carries a language, on the packets' way into the ring.
// It reassembles packets across writes, holding a partial one back until
// the next write completes it, and passes everything through untouched once
// sync is lost (as tsIndex stops indexing).
type tsRewriter struct {
	lang   Language
	pkt    [packetSize]byte
	filled int
	lost   bool
	pmtPID int
	out    []byte
}

func newTSRewriter(l Language) *tsRewriter { return &tsRewriter{lang: l, pmtPID: -1} }

// write returns p's complete packets, PMTs rewritten. The result aliases
// w's own buffer, valid until the next write; p is never modified.
func (w *tsRewriter) write(p []byte) []byte {
	if len(w.lang.Code) != 3 || w.lost {
		return p
	}
	w.out = w.out[:0]
	for len(p) > 0 {
		n := copy(w.pkt[w.filled:], p)
		w.filled += n
		p = p[n:]
		if w.filled < packetSize {
			break
		}
		w.filled = 0
		if w.pkt[0] != 0x47 {
			w.lost = true
			w.out = append(w.out, w.pkt[:]...)
			return append(w.out, p...)
		}
		at := len(w.out)
		w.out = append(w.out, w.pkt[:]...)
		w.packet(w.out[at : at+packetSize])
	}
	return w.out
}

// packet reads a PAT for the PMT's PID and rewrites a PMT in place. A PMT
// whose payload has no room for a new descriptor takes it from the
// adaptation field's stuffing, moving the payload up.
func (w *tsRewriter) packet(p []byte) {
	pid := int(p[1]&0x1f)<<8 | int(p[2])
	if p[1]&0x40 == 0 || (pid != 0 && pid != w.pmtPID) {
		return
	}
	off := 4
	if p[3]&0x20 != 0 {
		off = 5 + int(p[4])
	}
	if p[3]&0x10 == 0 || off >= packetSize {
		return
	}
	payload := p[off:]
	switch pid {
	case 0:
		if pmt, ok := patPMTPID(payload); ok {
			w.pmtPID = pmt
		}
	case w.pmtPID:
		_, short := rewritePMT(payload, w.lang)
		if short == 0 || p[3]&0x20 == 0 {
			return
		}
		if stuffing := adaptationStuffing(p); stuffing >= short {
			copy(p[off-short:], payload) // the payload moves up; its tail is now stuffing
			for i := packetSize - short; i < packetSize; i++ {
				p[i] = 0xff
			}
			p[4] -= byte(short)
			rewritePMT(p[off-short:], w.lang)
		}
	}
}

// adaptationStuffing is how many stuffing bytes the packet's adaptation
// field carries past its flags and optional fields.
func adaptationStuffing(p []byte) int {
	alen := int(p[4])
	if alen < 1 || 5+alen > packetSize {
		return 0
	}
	flags := p[5]
	used := 1 // the flags byte
	if flags&0x10 != 0 {
		used += 6 // PCR
	}
	if flags&0x08 != 0 {
		used += 6 // OPCR
	}
	if flags&0x04 != 0 {
		used++ // splice countdown
	}
	if flags&0x02 != 0 && 5+used < packetSize {
		used += 1 + int(p[5+used]) // private data
	}
	if flags&0x01 != 0 && 5+used < packetSize {
		used += 1 + int(p[5+used]) // extension
	}
	if used > alen {
		return 0
	}
	return alen - used
}

// trackKind is what a PMT elementary stream carries, as far as a language
// concerns it.
type trackKind int

const (
	trackNone trackKind = iota
	trackAudio
	trackSubtitle
)

// rewritePMT makes every audio and subtitle elementary stream of the PMT
// section starting in payload declare lang: an ISO 639 descriptor (tag
// 0x0a), and the language codes inside a DVB subtitling (0x59) or teletext
// (0x56) descriptor, naming an undetermined language are overwritten; a
// stream with no ISO 639 descriptor gets one when the payload's stuffing
// has room for its six bytes; and with Force a declared language is replaced
// too. A section that spans packets is left alone. It reports whether it
// changed anything (the CRC is recomputed when it did) and how many bytes
// of room it lacked for the descriptors it could not add.
func rewritePMT(payload []byte, lang Language) (changed bool, short int) {
	if len(lang.Code) != 3 || len(payload) < 1 || 1+int(payload[0]) >= len(payload) {
		return false, 0
	}
	s := payload[1+int(payload[0]):]
	if len(s) < 12 || s[0] != 0x02 {
		return false, 0
	}
	secLen := int(s[1]&0x0f)<<8 | int(s[2])
	end := 3 + secLen // the section's end, CRC included
	if end > len(s) || end < 16 {
		return false, 0
	}
	room := len(s) - end
	set := func(code []byte) {
		if (lang.Force || undetermined(code)) && string(code) != lang.Code {
			copy(code, lang.Code)
			changed = true
		}
	}
	i := 12 + (int(s[10]&0x0f)<<8 | int(s[11]))
	for i+5 <= end-4 {
		streamType := s[i]
		esLen := int(s[i+3]&0x0f)<<8 | int(s[i+4])
		descStart, descEnd := i+5, i+5+esLen
		if descEnd > end-4 {
			break
		}
		if kind := streamTrack(streamType, s[descStart:descEnd]); kind != trackNone {
			found := false
			for j := descStart; j+2 <= descEnd; {
				tag, l := s[j], int(s[j+1])
				body := j + 2
				if body+l > descEnd {
					break
				}
				switch {
				case tag == 0x0a:
					found = true
					for k := body; k+4 <= body+l; k += 4 {
						set(s[k : k+3])
					}
				case tag == 0x59 && kind == trackSubtitle: // DVB subtitling: 8 bytes per language
					for k := body; k+8 <= body+l; k += 8 {
						set(s[k : k+3])
					}
				case tag == 0x56 && kind == trackSubtitle: // teletext: 5 bytes per language
					for k := body; k+5 <= body+l; k += 5 {
						set(s[k : k+3])
					}
				}
				j = body + l
			}
			switch {
			case found:
			case room >= 6:
				copy(s[descEnd+6:end+6], s[descEnd:end]) // the rest of the section, CRC included, moves up
				copy(s[descEnd:descEnd+6], []byte{0x0a, 0x04, lang.Code[0], lang.Code[1], lang.Code[2], 0x00})
				esLen += 6
				s[i+3], s[i+4] = s[i+3]&0xf0|byte(esLen>>8), byte(esLen)
				secLen += 6
				s[1], s[2] = s[1]&0xf0|byte(secLen>>8), byte(secLen)
				end, room, descEnd = end+6, room-6, descEnd+6
				changed = true
			default:
				short += 6
			}
		}
		i = descEnd
	}
	if changed {
		binary.BigEndian.PutUint32(s[end-4:end], mpegCRC32(s[:end-4]))
	}
	return changed, short
}

// streamTrack classifies a PMT elementary stream: audio by its stream type,
// or for a private PES by an AC-3, E-AC-3, DTS or AAC descriptor or a
// registration naming one; a subtitle by a DVB subtitling or teletext
// descriptor; anything else carries no language.
func streamTrack(streamType byte, descriptors []byte) trackKind {
	switch streamType {
	case 0x03, 0x04, 0x0f, 0x11, 0x1c, 0x81, 0x82, 0x83, 0x84, 0x85, 0x86, 0x87:
		return trackAudio
	case 0x06:
	default:
		return trackNone
	}
	for j := 0; j+2 <= len(descriptors); {
		tag, l := descriptors[j], int(descriptors[j+1])
		if j+2+l > len(descriptors) {
			return trackNone
		}
		switch tag {
		case 0x6a, 0x7a, 0x7b, 0x7c:
			return trackAudio
		case 0x59, 0x56:
			return trackSubtitle
		case 0x05:
			if l >= 4 {
				switch string(descriptors[j+2 : j+6]) {
				case "AC-3", "DTS1", "DTS2", "DTS3", "Opus", "BSSD":
					return trackAudio
				}
			}
		}
		j += 2 + l
	}
	return trackNone
}

// undetermined reports whether a descriptor's three letters name no
// language: ISO 639-2's und, mis and zxx, or no letters at all.
func undetermined(code []byte) bool {
	switch string(code) {
	case "und", "mis", "zxx", "\x00\x00\x00", "   ":
		return true
	}
	for _, c := range code {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return true
		}
	}
	return false
}

// mpegCRC32 is the CRC a PSI section ends with: MPEG-2's, polynomial
// 0x04C11DB7, initial 0xFFFFFFFF, neither reflected nor inverted.
func mpegCRC32(b []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, c := range b {
		crc = crc<<8 ^ crcTable[byte(crc>>24)^c]
	}
	return crc
}

var crcTable = func() (t [256]uint32) {
	for i := range t {
		c := uint32(i) << 24
		for range 8 {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04c11db7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()
