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

package download

import (
	"bytes"
	"encoding/base64"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMaxPayloadFitsOneNATSMessage is the reason this cap is 4 MiB and not
// pkg/torznab's 8 MiB. DownloadResponse.Bytes is base64 in JSON and the whole
// reply is ONE NATS message; the broker's max_payload is 8Mi. An 8 MiB body
// would fail AFTER the fetch, having already spent a possibly one-shot
// download link.
func TestMaxPayloadFitsOneNATSMessage(t *testing.T) {
	const brokerMaxPayload = 8 << 20
	encoded := base64.StdEncoding.EncodedLen(MaxPayloadBytes)
	const envelopeOverhead = 4 << 10
	require.Less(t, encoded+envelopeOverhead, brokerMaxPayload,
		"a full-size body must marshal into one NATS message")
	require.Greater(t, MaxPayloadBytes, 1<<20, "1 MiB would reject real .nzb files")
}

// TestBrokerMaxPayloadIsStillEightMebibytes reads the broker's own config, so
// the arithmetic above cannot silently stop holding when someone retunes NATS.
func TestBrokerMaxPayloadIsStillEightMebibytes(t *testing.T) {
	raw, err := os.ReadFile("../../config/nats/configmap.yaml")
	require.NoError(t, err)
	require.Contains(t, string(raw), "max_payload: 8Mi",
		"the broker's max_payload changed; re-derive MaxPayloadBytes from it")
}

func TestReadPayloadCapsTheBody(t *testing.T) {
	body := bytes.Repeat([]byte("d"), MaxPayloadBytes+1)
	_, err := readPayload(io.NopCloser(bytes.NewReader(body)))
	require.ErrorIs(t, err, ErrResponseTooLarge)
	require.NotContains(t, err.Error(), "http", "the cap error must never carry a URL")
}

func TestReadPayloadAcceptsExactlyTheLimit(t *testing.T) {
	body := bytes.Repeat([]byte("d"), MaxPayloadBytes)
	got, err := readPayload(io.NopCloser(bytes.NewReader(body)))
	require.NoError(t, err)
	require.Len(t, got, MaxPayloadBytes)
}

func TestReadPayloadRejectsAnEmptyBody(t *testing.T) {
	_, err := readPayload(io.NopCloser(bytes.NewReader(nil)))
	require.ErrorIs(t, err, errEmptyBody)
}

func TestSniffKind(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
		want payloadKind
	}{
		{"bencode dict", []byte("d8:announce30:http://tr.example/announceee"), kindTorrent},
		{"nzb with prolog", []byte("<?xml version=\"1.0\"?>\n<nzb xmlns=\"...\">"), kindNZB},
		{"nzb without prolog", []byte("<nzb><file/></nzb>"), kindNZB},
		{"leading whitespace is skipped", []byte("\n\n  <?xml version=\"1.0\"?><nzb/>"), kindNZB},
		{"html doctype", []byte("<!DOCTYPE html><html><body>login"), kindHTML},
		{"html tag", []byte("<HTML><head>"), kindHTML},
		{"garbage", []byte{0x00, 0x01, 0x02}, kindUnknown},
		{"empty", nil, kindUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, sniffKind(tc.body)) })
	}
}

func TestContentTypeForPrefersTheSniffOverAWrongHeader(t *testing.T) {
	// Real trackers serve .torrent as text/html all the time.
	require.Equal(t, "application/x-bittorrent", contentTypeFor("text/html; charset=utf-8", kindTorrent))
	require.Equal(t, "application/x-nzb", contentTypeFor("", kindNZB))
	// An unrecognised body falls back to the header, parsed and stripped of
	// parameters -- never echoed raw.
	require.Equal(t, "application/octet-stream", contentTypeFor("application/octet-stream; x=1", kindUnknown))
	require.Equal(t, "application/octet-stream", contentTypeFor("!! not a media type", kindUnknown))
	require.LessOrEqual(t, len(contentTypeFor(strings.Repeat("a/b;", 500), kindUnknown)), maxContentTypeChars)
}
