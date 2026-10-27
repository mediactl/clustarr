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
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pmtWith is a PMT packet whose audio stream (AAC on testAudioPID) carries
// the descriptors given, and a video stream before it.
func pmtWith(audioDescriptors []byte, extra ...[]byte) []byte {
	body := []byte{0xe0 | testVideoPID>>8, testVideoPID & 0xff, 0xf0, 0x00}
	body = append(body, 0x1b, 0xe0|testVideoPID>>8, testVideoPID&0xff, 0xf0, 0x00)
	body = append(body, 0x0f, 0xe0|testAudioPID>>8, testAudioPID&0xff, 0xf0|byte(len(audioDescriptors)>>8), byte(len(audioDescriptors)))
	body = append(body, audioDescriptors...)
	for _, e := range extra {
		body = append(body, e...)
	}
	return tsPacket(testPMTPID, true, false, section(0x02, body))
}

// iso639 is an ISO 639 language descriptor naming code.
func iso639(code string) []byte { return append([]byte{0x0a, 0x04}, append([]byte(code), 0x00)...) }

// pmtSection is the PMT section a packet carries, CRC included.
func pmtSection(t *testing.T, pkt []byte) []byte {
	t.Helper()
	off := 4
	if pkt[3]&0x20 != 0 {
		off = 5 + int(pkt[4])
	}
	payload := pkt[off:]
	s := payload[1+int(payload[0]):]
	n := 3 + (int(s[1]&0x0f)<<8 | int(s[2]))
	return s[:n]
}

// descriptorsOf is the audio stream's descriptor loop in a PMT section.
func descriptorsOf(s []byte, pid int) []byte {
	i := 12 + (int(s[10]&0x0f)<<8 | int(s[11]))
	for i+5 <= len(s)-4 {
		el := int(s[i+3]&0x0f)<<8 | int(s[i+4])
		if int(s[i+1]&0x1f)<<8|int(s[i+2]) == pid {
			return s[i+5 : i+5+el]
		}
		i += 5 + el
	}
	return nil
}

func crcHolds(t *testing.T, s []byte) {
	t.Helper()
	assert.Equal(t, mpegCRC32(s[:len(s)-4]), binary.BigEndian.Uint32(s[len(s)-4:]), "the section's CRC")
}

// The reason for the rewrite: tvload.win's ESPN declares its audio "und",
// which Plex shows as Unknown; the relay makes it the provider's language and
// recomputes the section's CRC.
func TestAnUndeterminedAudioLanguageBecomesTheProviders(t *testing.T) {
	w := newTSRewriter(Fill("eng"))
	out := w.write(cat(patPacket(), pmtWith(iso639("und"))))
	require.Len(t, out, 2*packetSize)
	s := pmtSection(t, out[packetSize:])
	assert.Equal(t, iso639("eng"), descriptorsOf(s, testAudioPID))
	crcHolds(t, s)
	assert.Equal(t, patPacket(), out[:packetSize], "the PAT is untouched")
}

// A stream that names no language gets a descriptor, six bytes the
// section and the stream's loop grow by, taken from the packet's stuffing.
func TestAMissingLanguageDescriptorIsAdded(t *testing.T) {
	w := newTSRewriter(Fill("eng"))
	before := pmtWith(nil)
	out := w.write(cat(patPacket(), before))
	s := pmtSection(t, out[packetSize:])
	assert.Equal(t, iso639("eng"), descriptorsOf(s, testAudioPID))
	assert.Len(t, s, len(pmtSection(t, before))+6)
	assert.Empty(t, descriptorsOf(s, testVideoPID), "the video stream gets none")
	crcHolds(t, s)
}

// The provider's language fills a gap only; a channel's own language is
// forced over what the stream says.
func TestADeclaredLanguageIsKeptUnlessForced(t *testing.T) {
	out := newTSRewriter(Fill("eng")).write(cat(patPacket(), pmtWith(iso639("spa"))))
	assert.Equal(t, iso639("spa"), descriptorsOf(pmtSection(t, out[packetSize:]), testAudioPID))

	out = newTSRewriter(Language{Code: "eng", Force: true}).write(cat(patPacket(), pmtWith(iso639("spa"))))
	s := pmtSection(t, out[packetSize:])
	assert.Equal(t, iso639("eng"), descriptorsOf(s, testAudioPID))
	crcHolds(t, s)
}

// The language names the subtitles too (owner, 2026-10-27): a DVB
// subtitling descriptor's undetermined code is rewritten in place, and the
// stream gets an ISO 639 descriptor beside it.
func TestASubtitleStreamsLanguageIsRewrittenToo(t *testing.T) {
	const subPID = 0x102
	dvb := []byte{0x59, 0x08, 'u', 'n', 'd', 0x10, 0x00, 0x01, 0x00, 0x02}
	sub := append([]byte{0x06, 0xe0 | subPID>>8, subPID & 0xff, 0xf0, byte(len(dvb))}, dvb...)
	w := newTSRewriter(Fill("eng"))
	out := w.write(cat(patPacket(), pmtWith(iso639("eng"), sub)))
	s := pmtSection(t, out[packetSize:])
	d := descriptorsOf(s, subPID)
	assert.Equal(t, []byte{0x59, 0x08, 'e', 'n', 'g', 0x10, 0x00, 0x01, 0x00, 0x02}, d[:10])
	assert.Equal(t, iso639("eng"), d[10:])
	crcHolds(t, s)
}

// Packets arrive in reads that fall anywhere; the rewriter holds a partial
// packet back and rewrites it once whole, and never writes into the
// caller's buffer.
func TestPacketsSplitAcrossWritesAreRewrittenWhole(t *testing.T) {
	w := newTSRewriter(Fill("eng"))
	in := cat(patPacket(), pmtWith(iso639("und")), h264Key())
	snapshot := bytes.Clone(in)
	var out []byte
	for i := 0; i < len(in); i += 100 {
		out = append(out, w.write(in[i:min(i+100, len(in))])...)
	}
	require.Len(t, out, len(in))
	assert.Equal(t, snapshot, in, "the input is never modified")
	assert.Equal(t, iso639("eng"), descriptorsOf(pmtSection(t, out[packetSize:2*packetSize]), testAudioPID))
	assert.Equal(t, h264Key(), out[2*packetSize:])
}

// Lost sync passes everything through, as the index stops indexing.
func TestLostSyncPassesBytesThrough(t *testing.T) {
	w := newTSRewriter(Fill("eng"))
	junk := bytes.Repeat([]byte{0x11}, packetSize)
	in := cat(patPacket(), junk, pmtWith(iso639("und")))
	out := w.write(in)
	assert.Equal(t, in, out)
	assert.True(t, w.lost)
}

// No language, no rewrite: the bytes come back as they are.
func TestNoLanguageRewritesNothing(t *testing.T) {
	in := cat(patPacket(), pmtWith(iso639("und")))
	assert.Equal(t, in, newTSRewriter(Language{}).write(in))
}

// A section that fills its packet has no room for a new descriptor and is
// left as it is rather than cut.
func TestNoRoomLeavesTheSectionAlone(t *testing.T) {
	filler := make([]byte, 0, 160)
	for len(filler) < 150 {
		filler = append(filler, 0x05, 0x04, 'X', 'X', 'X', 'X') // registration descriptors on the audio stream
	}
	pkt := pmtWith(filler)
	before := bytes.Clone(pkt)
	out := newTSRewriter(Fill("eng")).write(cat(patPacket(), pkt))
	assert.Equal(t, before, out[packetSize:])
}

func TestMPEGCRC32MatchesTheKnownCheckValue(t *testing.T) {
	// The CRC-32/MPEG-2 check value for "123456789" is 0x0376E6E7.
	assert.Equal(t, uint32(0x0376e6e7), mpegCRC32([]byte("123456789")))
}
