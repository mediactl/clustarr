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

import "bytes"

// tsIndex reads an upstream's MPEG-TS packets as they are written, for where
// a new viewer may start: the latest PAT and PMT packets, and the first
// packet of the latest video keyframe -- an access unit carrying its
// parameter sets, which a decoder can begin at. It indexes nothing once a
// packet lacks its sync byte, until reset at the next connection.
type tsIndex struct {
	pkt    [packetSize]byte
	filled int   // bytes of pkt so far
	pos    int64 // stream position of pkt[0]
	lost   bool  // sync was lost: nothing more is indexed

	pmtPID   int // -1 until a PAT names one
	videoPID int // -1 until the PMT names one
	codec    byte
	pat, pmt []byte
	key      int64 // the latest keyframe's position, -1 when none
}

func newTSIndex(at int64) tsIndex {
	return tsIndex{pos: at, pmtPID: -1, videoPID: -1, key: -1}
}

// write reads p, which continues the stream at the index's position.
func (x *tsIndex) write(p []byte) {
	for len(p) > 0 {
		n := copy(x.pkt[x.filled:], p)
		x.filled += n
		p = p[n:]
		if x.filled < packetSize {
			return
		}
		if !x.lost {
			x.packet(x.pkt[:])
		}
		x.pos += packetSize
		x.filled = 0
	}
}

func (x *tsIndex) packet(p []byte) {
	if p[0] != 0x47 {
		x.lost, x.key = true, -1
		return
	}
	pid := int(p[1]&0x1f)<<8 | int(p[2])
	start := p[1]&0x40 != 0
	if !start || (pid != 0 && pid != x.pmtPID && pid != x.videoPID) {
		return
	}
	off, rai := 4, false
	if p[3]&0x20 != 0 {
		alen := int(p[4])
		rai = alen > 0 && p[5]&0x40 != 0
		off = 5 + alen
	}
	if p[3]&0x10 == 0 || off >= packetSize {
		return
	}
	payload := p[off:]
	switch pid {
	case 0:
		if pmt, ok := patPMTPID(payload); ok {
			x.pmtPID, x.pat = pmt, bytes.Clone(p)
		}
	case x.pmtPID:
		if video, codec, ok := pmtVideo(payload); ok {
			x.videoPID, x.codec, x.pmt = video, codec, bytes.Clone(p)
		}
	case x.videoPID:
		if keyframe(x.codec, pesData(payload), rai) {
			x.key = x.pos
		}
	}
}

// psiSection is the PSI section a payload starting one carries, whole within
// the packet, or nil.
func psiSection(payload []byte, tableID byte) []byte {
	if len(payload) < 1 || int(payload[0])+1 >= len(payload) {
		return nil
	}
	s := payload[1+int(payload[0]):]
	if len(s) < 3 || s[0] != tableID {
		return nil
	}
	end := 3 + (int(s[1]&0x0f)<<8 | int(s[2]))
	if end > len(s) || end < 12 {
		return nil
	}
	return s[:end-4] // the CRC dropped
}

// patPMTPID is the first program's PMT PID.
func patPMTPID(payload []byte) (int, bool) {
	s := psiSection(payload, 0x00)
	if s == nil {
		return 0, false
	}
	for i := 8; i+4 <= len(s); i += 4 {
		if s[i] != 0 || s[i+1] != 0 { // program 0 is the network PID
			return int(s[i+2]&0x1f)<<8 | int(s[i+3]), true
		}
	}
	return 0, false
}

// pmtVideo is the program's first video stream: its PID and stream type.
func pmtVideo(payload []byte) (int, byte, bool) {
	s := psiSection(payload, 0x02)
	if len(s) < 12 {
		return 0, 0, false
	}
	for i := 12 + (int(s[10]&0x0f)<<8 | int(s[11])); i+5 <= len(s); i += 5 + (int(s[i+3]&0x0f)<<8 | int(s[i+4])) {
		switch s[i] {
		case 0x01, 0x02, 0x1b, 0x24: // MPEG-1, MPEG-2, H.264, HEVC video
			return int(s[i+1]&0x1f)<<8 | int(s[i+2]), s[i], true
		}
	}
	return 0, 0, false
}

// pesData is the elementary stream data in a PES packet's first TS packet.
func pesData(payload []byte) []byte {
	if len(payload) < 9 || payload[0] != 0 || payload[1] != 0 || payload[2] != 1 {
		return nil
	}
	if end := 9 + int(payload[8]); end < len(payload) {
		return payload[end:]
	}
	return nil
}

// keyframe reports whether an access unit starting with data is one a
// decoder can begin at: its parameter sets with an IDR picture (or the
// packet's random-access flag), or an MPEG-1/2 sequence header.
func keyframe(codec byte, data []byte, rai bool) bool {
	params, idr := false, false
	for i := 0; i+3 < len(data); i++ {
		if data[i] != 0 || data[i+1] != 0 || data[i+2] != 1 {
			continue
		}
		h := data[i+3]
		switch codec {
		case 0x01, 0x02:
			if h == 0xb3 {
				return true
			}
		case 0x1b:
			switch h & 0x1f {
			case 7:
				params = true
			case 5:
				idr = true
			}
		case 0x24:
			switch t := h >> 1 & 0x3f; {
			case t == 32 || t == 33:
				params = true
			case t >= 16 && t <= 21:
				idr = true
			}
		}
		i += 2
	}
	return params && (idr || rai)
}
