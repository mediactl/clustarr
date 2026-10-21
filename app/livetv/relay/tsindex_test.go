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

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testPMTPID   = 0x1000
	testVideoPID = 0x100
	testAudioPID = 0x101
)

// tsPacket is one 188-byte packet on pid carrying payload, stuffed with an
// adaptation field (random access when rai) so the payload ends the packet.
func tsPacket(pid int, pusi, rai bool, payload []byte) []byte {
	p := make([]byte, 0, packetSize)
	b1 := byte(pid>>8) & 0x1f
	if pusi {
		b1 |= 0x40
	}
	p = append(p, 0x47, b1, byte(pid))
	stuff := packetSize - 4 - len(payload)
	if stuff == 0 && !rai {
		p = append(p, 0x10)
		return append(p, payload...)
	}
	p = append(p, 0x30)
	if stuff < 2 {
		stuff = 2 // a flags byte at least; the payload is cut to fit
		payload = payload[:packetSize-4-stuff]
	}
	flags := byte(0)
	if rai {
		flags = 0x40
	}
	p = append(p, byte(stuff-1), flags)
	p = append(p, bytes.Repeat([]byte{0xff}, stuff-2)...)
	return append(p, payload...)
}

// section is a PSI section of tableID carrying body, with a zero CRC (the
// index does not check it).
func section(tableID byte, body []byte) []byte {
	n := 5 + len(body) + 4 // the fields after section_length, the body, the CRC
	s := []byte{0x00, tableID, 0xb0 | byte(n>>8)&0x0f, byte(n), 0x00, 0x01, 0xc1, 0x00, 0x00}
	s = append(s, body...)
	return append(s, 0, 0, 0, 0)
}

func patPacket() []byte {
	return tsPacket(0, true, false, section(0x00, []byte{0x00, 0x01, 0xe0 | testPMTPID>>8, testPMTPID & 0xff}))
}

func pmtPacket(videoType byte) []byte {
	body := []byte{0xe0 | testVideoPID>>8, testVideoPID & 0xff, 0xf0, 0x00} // PCR PID, no program info
	body = append(body, videoType, 0xe0|testVideoPID>>8, testVideoPID&0xff, 0xf0, 0x00)
	body = append(body, 0x0f, 0xe0|testAudioPID>>8, testAudioPID&0xff, 0xf0, 0x00)
	return tsPacket(testPMTPID, true, false, section(0x02, body))
}

// videoPacket starts a video PES whose first bytes carry the NAL headers
// given, each behind a start code.
func videoPacket(rai bool, nals ...byte) []byte {
	pes := []byte{0x00, 0x00, 0x01, 0xe0, 0x00, 0x00, 0x80, 0x80, 0x05, 0x21, 0x00, 0x01, 0x00, 0x01}
	for _, n := range nals {
		pes = append(pes, 0x00, 0x00, 0x00, 0x01, n, 0xaa, 0xbb)
	}
	return tsPacket(testVideoPID, true, rai, pes)
}

// fill is n packets continuing the current PES on pid.
func fill(pid, n int) []byte {
	var b []byte
	for range n {
		b = append(b, tsPacket(pid, false, false, bytes.Repeat([]byte{0x11}, packetSize-4))...)
	}
	return b
}

// h264Key is an H.264 IDR access unit's first packet: AUD, SPS, PPS, IDR.
func h264Key() []byte { return videoPacket(true, 0x09, 0x67, 0x68, 0x65) }

// h264Frame is a non-IDR access unit's first packet: AUD, SEI, slice.
func h264Frame() []byte { return videoPacket(false, 0x09, 0x06, 0x41) }

// writeChunked writes b in chunks that never fall on a packet boundary.
func writeChunked(r *ring, b []byte) {
	for len(b) > 0 {
		n := min(1000, len(b))
		r.Write(b[:n])
		b = b[n:]
	}
}

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// pastTheStart is a connection's first connectionStart bytes and more: its
// tables, a keyframe and a GOP's worth of packets after it.
func pastTheStart() []byte {
	return cat(patPacket(), pmtPacket(0x1b), h264Key(), fill(testVideoPID, connectionStart/packetSize+1))
}

func TestJoinNearTheConnectionsStartIsItsFirstByte(t *testing.T) {
	r := newRing(4 * connectionStart)
	writeChunked(r, cat(fill(0x11, 1), patPacket(), pmtPacket(0x1b), h264Key(), fill(testVideoPID, 30)))

	cur, tables := r.Join()

	assert.Equal(t, int64(0), cur, "a first viewer misses none of the connection")
	assert.Nil(t, tables)
}

func TestJoinStartsAtTheLatestKeyframeAfterTheTables(t *testing.T) {
	r := newRing(4 * connectionStart)
	head := cat(pastTheStart(), h264Frame(), fill(testVideoPID, 20), patPacket(), pmtPacket(0x1b))
	writeChunked(r, cat(head, h264Key(), fill(testVideoPID, 5), h264Frame(), fill(testAudioPID, 3)))

	cur, tables := r.Join()

	assert.Equal(t, int64(len(head)), cur, "the latest keyframe's packet")
	assert.Equal(t, cat(patPacket(), pmtPacket(0x1b)), tables)
}

func TestJoinFindsEachCodecsKeyframes(t *testing.T) {
	for name, tc := range map[string]struct {
		streamType byte
		key, frame []byte
	}{
		"h264 SPS and IDR":                   {0x1b, h264Key(), h264Frame()},
		"h264 SPS with a random-access flag": {0x1b, videoPacket(true, 0x09, 0x67, 0x68, 0x41), videoPacket(false, 0x09, 0x67, 0x68, 0x41)},
		"hevc VPS and IDR":                   {0x24, videoPacket(false, 0x46, 0x40, 0x42, 0x44, 0x26), videoPacket(false, 0x46, 0x02)},
		"mpeg-2 sequence header":             {0x02, videoPacket(false, 0xb3, 0xb8, 0x00), videoPacket(false, 0x00)},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRing(4 * connectionStart)
			head := cat(patPacket(), pmtPacket(tc.streamType), tc.frame, fill(testVideoPID, connectionStart/packetSize+1))
			writeChunked(r, cat(head, tc.key, fill(testVideoPID, 10), tc.frame, fill(testVideoPID, 10)))

			cur, tables := r.Join()

			assert.Equal(t, int64(len(head)), cur)
			assert.Equal(t, cat(patPacket(), pmtPacket(tc.streamType)), tables)
		})
	}
}

func TestJoinWellPastTheConnectionWithoutAKeyframeIsTheNewestPacket(t *testing.T) {
	r := newRing(4 * connectionStart)
	b := cat(patPacket(), pmtPacket(0x1b), h264Frame(), fill(testVideoPID, connectionStart/packetSize+10))
	writeChunked(r, cat(b, []byte{0x47, 0x01})) // and the start of one more

	cur, tables := r.Join()

	assert.Equal(t, int64(len(b)), cur)
	assert.Nil(t, tables)
}

func TestJoinIgnoresAKeyframeHalfABufferBack(t *testing.T) {
	r := newRing(64 << 10)
	b := cat(patPacket(), pmtPacket(0x1b), h264Key(), fill(testVideoPID, (32<<10)/packetSize+1))
	writeChunked(r, b)

	cur, tables := r.Join()

	assert.Equal(t, int64(len(b)), cur, "the newest packet: the keyframe is too far back to read safely")
	assert.Nil(t, tables)
}

func TestJoinNeverStartsBeforeAReopenedConnection(t *testing.T) {
	r := newRing(4 * connectionStart)
	first := cat(pastTheStart(), []byte{0x47, 0x00}) // cut mid-packet
	writeChunked(r, first)
	r.Realign()
	writeChunked(r, cat(h264Frame(), fill(testVideoPID, 10)))

	cur, tables := r.Join()

	assert.Equal(t, int64(len(first)), cur, "the reopened connection's first byte")
	assert.Nil(t, tables)

	writeChunked(r, cat(patPacket(), pmtPacket(0x1b), h264Key(), fill(testVideoPID, connectionStart/packetSize+1)))
	cur, tables = r.Join()
	assert.Equal(t, int64(len(first)+13*packetSize), cur, "its own keyframe")
	assert.Equal(t, cat(patPacket(), pmtPacket(0x1b)), tables)
}

func TestAStreamThatLosesSyncIsNoLongerIndexed(t *testing.T) {
	r := newRing(4 * connectionStart)
	b := cat(pastTheStart(), []byte{0x00, 0x47}, fill(testVideoPID, 3), patPacket(), pmtPacket(0x1b), h264Key(), fill(testVideoPID, 3))
	writeChunked(r, b)

	cur, tables := r.Join()

	assert.Equal(t, int64(len(b)-(len(b)%packetSize)), cur, "the newest packet, as for a stream with no keyframe")
	assert.Nil(t, tables)
}

func TestAFrameWithoutParameterSetsIsNoKeyframe(t *testing.T) {
	r := newRing(4 * connectionStart)
	b := cat(patPacket(), pmtPacket(0x1b), videoPacket(true, 0x09, 0x06, 0x41), fill(testVideoPID, connectionStart/packetSize+1))
	writeChunked(r, b)

	cur, tables := r.Join()

	assert.Equal(t, int64(len(b)), cur, "a random-access flag on a slice alone is not a start")
	assert.Nil(t, tables)
}

func TestTheTestPacketsAreWellFormed(t *testing.T) {
	for _, p := range [][]byte{patPacket(), pmtPacket(0x1b), h264Key(), h264Frame(), fill(testAudioPID, 1)} {
		require.Len(t, p, packetSize)
		require.Equal(t, byte(0x47), p[0])
	}
}
