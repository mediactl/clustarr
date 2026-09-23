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

package usenet

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Tensai75/nntp"
	"github.com/javi11/rapidyenc"
	"github.com/stretchr/testify/require"
)

func TestDotReaderHandsOutTheWireBytesVerbatim(t *testing.T) {
	// rapidyenc is a RAW NNTP decoder: it un-stuffs dots itself and finds the
	// end of the article by the literal "\r\n.\r\n". A reader that cooked the
	// stream first -- as Tensai75/nntp's bodyReader does -- breaks every
	// decode, so this asserts the bytes come through untouched.
	wire := "=ybegin part=1\r\n..stuffed\r\nplain\r\n.\r\n222 next response\r\n"
	br := bufio.NewReader(strings.NewReader(wire))

	dr := &dotReader{br: br, limit: 1 << 20}
	got, err := io.ReadAll(dr)
	require.NoError(t, err)
	require.Equal(t, "=ybegin part=1\r\n..stuffed\r\nplain\r\n.\r\n", string(got),
		"dots must stay stuffed and the terminator must be included")

	// And the connection is positioned at the next response, not inside the
	// one just read.
	rest, err := io.ReadAll(br)
	require.NoError(t, err)
	require.Equal(t, "222 next response\r\n", string(rest))
}

func TestDotReaderAcceptsAOneByteAtATimeReader(t *testing.T) {
	// The terminator straddles reads on a real socket. iotest.OneByteReader
	// forces every boundary the state machine has.
	wire := "line one\r\n.dot\r\n.\r\n"
	br := bufio.NewReader(oneByteReader{strings.NewReader(wire)})
	dr := &dotReader{br: br, limit: 1 << 20}
	got, err := io.ReadAll(dr)
	require.NoError(t, err)
	require.Equal(t, wire, string(got))
}

type oneByteReader struct{ r io.Reader }

func (o oneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return o.r.Read(p[:1])
}

func TestDotReaderStopsAtItsLimit(t *testing.T) {
	// A malicious or broken server must not be able to decide how much memory
	// a worker uses -- the same rule every HTTP body in this repo follows.
	body := strings.Repeat("A", 4096) + "\r\n.\r\n"
	br := bufio.NewReader(strings.NewReader(body))
	dr := &dotReader{br: br, limit: 64}

	_, err := io.ReadAll(dr)
	require.ErrorIs(t, err, ErrArticleTooLarge)
	require.True(t, dr.overflow, "the connection must be marked unusable: the rest of the body is still on the wire")
}

func TestDecodeArticleRoundTripsAndVerifiesTheCRC(t *testing.T) {
	payload := partPayload(11, 2000)
	art := stubArticle{data: payload, meta: rapidyenc.Meta{
		FileName: "movie.mkv", FileSize: 6000, PartNumber: 2, TotalParts: 3,
		Offset: 2000, PartSize: int64(len(payload)),
	}}
	encoded, err := encodePart(art)
	require.NoError(t, err)

	wire := append(dotStuff(encoded), []byte(".\r\n")...)
	got, meta, err := decodeArticle(bytes.NewReader(wire), defaultMaxArticleBytes)
	require.NoError(t, err)
	require.Equal(t, payload, got)
	require.Equal(t, int64(2000), meta.Offset,
		"the write offset comes from =ypart begin, not from the NZB segment number")
	require.Equal(t, "movie.mkv", meta.FileName)
}

func TestDecodeArticleRejectsACorruptedPart(t *testing.T) {
	// pcrc32 is verified as the part decodes, so a provider serving corrupt
	// bytes is caught here rather than by par2 half an hour later.
	payload := partPayload(12, 1000)
	art := stubArticle{data: payload, meta: rapidyenc.Meta{
		FileName: "movie.mkv", FileSize: 1000, PartNumber: 1, TotalParts: 1,
		Offset: 0, PartSize: int64(len(payload)),
	}}
	encoded, err := encodePart(art)
	require.NoError(t, err)

	// Flip one byte in the middle of the encoded payload.
	corrupt := append([]byte(nil), encoded...)
	corrupt[len(corrupt)/2] ^= 0x20
	wire := append(dotStuff(corrupt), []byte(".\r\n")...)

	_, _, err = decodeArticle(bytes.NewReader(wire), defaultMaxArticleBytes)
	require.Error(t, err)
}

func TestDecodeArticleRefusesAnOversizedBody(t *testing.T) {
	payload := partPayload(13, 4096)
	art := stubArticle{data: payload, meta: rapidyenc.Meta{
		FileName: "movie.mkv", FileSize: 4096, PartNumber: 1, TotalParts: 1,
		Offset: 0, PartSize: int64(len(payload)),
	}}
	encoded, err := encodePart(art)
	require.NoError(t, err)
	wire := append(dotStuff(encoded), []byte(".\r\n")...)

	_, _, err = decodeArticle(bytes.NewReader(wire), 1024)
	require.ErrorIs(t, err, ErrArticleTooLarge)
}

func TestValidMessageIDRejectsInjectionAndBrackets(t *testing.T) {
	require.True(t, validMessageID("abc123@news.example"))
	require.False(t, validMessageID(""))
	require.False(t, validMessageID("evil\r\nQUIT"), "CRLF would inject a second command")
	require.False(t, validMessageID("with space@x"))
	require.False(t, validMessageID("<already@bracketed>"), "the wire layer adds the brackets")
	require.False(t, validMessageID(strings.Repeat("a", 251)))
}

func TestStatusErrorClassifiesAndKeepsTheServersOwnAnswer(t *testing.T) {
	err := statusError(430, "no such article")
	require.ErrorIs(t, err, errArticleNotFound)
	require.False(t, fatalForConn(err), "a 430 leaves the stream framed")

	var wire nntp.Error
	require.True(t, errors.As(err, &wire), "the server's own status must stay reachable")
	require.Equal(t, uint(430), wire.Code)

	require.ErrorIs(t, statusError(502, "too many connections"), errConnectionRefusedByServer)
	require.ErrorIs(t, statusError(400, "service discontinued"), errConnectionRefusedByServer)
	require.ErrorIs(t, statusError(481, "bad password"), errAuth)
	require.ErrorIs(t, statusError(999, "what"), errProtocol)
	require.True(t, fatalForConn(statusError(502, "x")))
}

func TestBitsetRoundTrips(t *testing.T) {
	b := newBitset(130)
	require.Equal(t, 0, b.count())
	b.set(0)
	b.set(64)
	b.set(129)
	require.True(t, b.has(0))
	require.True(t, b.has(64))
	require.True(t, b.has(129))
	require.False(t, b.has(1))
	require.Equal(t, 3, b.count())

	// Out-of-range access must not panic: a manifest from an older build can
	// carry a shorter bitset than the NZB now parses to.
	b.set(1000)
	require.False(t, b.has(1000))
	require.False(t, b.has(-1))
}
