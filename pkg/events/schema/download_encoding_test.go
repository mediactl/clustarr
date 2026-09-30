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

package schema

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"
)

// nzbOfSize is an .nzb shaped like a real post -- one <segment> per ~700 KB
// article, each a random 32-hex message id -- grown to at least n bytes, so
// it compresses about as well as one (The Godfather Part II's 3.8 MB .nzb
// gzips to 955 KB).
func nzbOfSize(n int) []byte {
	r := rand.New(rand.NewPCG(1, 2))
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<nzb><file subject=\"x.part001.rar\"><segments>\n")
	for i := 1; b.Len() < n; i++ {
		fmt.Fprintf(&b, "<segment bytes=\"%d\" number=\"%d\">%016x%016x@%x.local</segment>\n",
			716800+r.IntN(20), i, r.Uint64(), r.Uint64(), r.Uint32())
	}
	b.WriteString("</segments></file></nzb>\n")
	return b.Bytes()
}

// A body the size of a big 1080p encode's .nzb crosses the broker in gzip
// and comes back byte for byte (2026-09-30: The Godfather's .nzb was over
// the old 4 MiB inline cap and its Download never left Assigned).
func TestDownloadResponseRoundTripsALargeNZBCompressed(t *testing.T) {
	body := nzbOfSize(12 << 20)
	wire := DownloadResponse{Bytes: body, ContentType: "application/x-nzb"}.ForWire()

	require.Empty(t, wire.Error)
	require.Equal(t, DownloadEncodingGzip, wire.Encoding)
	require.LessOrEqual(t, len(wire.Bytes), MaxDownloadWireBytes)
	msg, err := json.Marshal(wire)
	require.NoError(t, err)
	require.Less(t, len(msg), 8<<20, "the reply must fit the broker's 8Mi max_payload")

	var got DownloadResponse
	require.NoError(t, json.Unmarshal(msg, &got))
	payload, err := got.Payload(32 << 20)
	require.NoError(t, err)
	require.Equal(t, body, payload)
}

func TestDownloadResponseLeavesASmallBodyAsItIs(t *testing.T) {
	body := []byte("d8:announce...e")
	wire := DownloadResponse{Bytes: body}.ForWire()
	require.Empty(t, wire.Encoding)
	require.Equal(t, body, wire.Bytes)
	payload, err := wire.Payload(1 << 20)
	require.NoError(t, err)
	require.Equal(t, body, payload)
}

// A body that is still over the wire budget once compressed is refused in
// the reply, never sent: the broker would drop it and the engine would wait
// out its deadline.
func TestDownloadResponseRefusesABodyThatDoesNotCompressUnderTheBudget(t *testing.T) {
	body := make([]byte, MaxDownloadWireBytes+1)
	r := rand.New(rand.NewPCG(3, 4))
	for i := range body {
		body[i] = byte(r.Uint32())
	}
	wire := DownloadResponse{Bytes: body, ContentType: "application/x-bittorrent"}.ForWire()
	require.Contains(t, wire.Error, "too large")
	require.Empty(t, wire.Bytes)
	require.Empty(t, wire.ContentType)
}

// A gzip bomb is cut off at the caller's cap, not inflated whole.
func TestDownloadResponsePayloadCapsTheDecompressedSize(t *testing.T) {
	var z bytes.Buffer
	w := gzip.NewWriter(&z)
	_, err := w.Write(make([]byte, 64<<20))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	_, err = DownloadResponse{Bytes: z.Bytes(), Encoding: DownloadEncodingGzip}.Payload(32 << 20)
	require.ErrorIs(t, err, ErrDownloadPayloadTooLarge)
}

func TestDownloadResponsePayloadRefusesAnUnknownEncoding(t *testing.T) {
	_, err := DownloadResponse{Bytes: []byte("x"), Encoding: "br"}.Payload(1 << 20)
	require.ErrorContains(t, err, `encoding "br"`)
}

// The wire budget still fits one NATS message after base64 and the
// envelope, as app/indexer/download's inline cap did before.
// It uses most of that room -- about 25 MiB of .nzb at gzip's 4.3x -- with
// a margin kept for the envelope and headers; natsbus's
// TestAFullDownloadWireBudgetFitsTheBrokersMaxPayload proves it on a real
// server.
func TestMaxDownloadWireBytesFitsOneNATSMessage(t *testing.T) {
	const brokerMaxPayload, margin = 8 << 20, 256 << 10
	require.LessOrEqual(t, base64.StdEncoding.EncodedLen(MaxDownloadWireBytes)+margin, brokerMaxPayload)
	require.GreaterOrEqual(t, MaxDownloadWireBytes, 23<<18, "at least 5.75 MiB: the broker's room, not the old 4 MiB")
}
