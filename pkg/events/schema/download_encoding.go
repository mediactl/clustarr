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
	"errors"
	"fmt"
	"io"
)

// DownloadEncodingGzip is DownloadResponse.Encoding for a gzip body.
const DownloadEncodingGzip = "gzip"

// MaxDownloadWireBytes is the most DownloadResponse.Bytes may carry on the
// wire, compressed or not: 5.75 MiB, which base64 makes about 7.67 MiB and
// leaves over 256 KiB of the broker's 8Mi max_payload for the envelope and
// headers (natsbus.TestAFullDownloadWireBudgetFitsTheBrokersMaxPayload sends
// it through a real server; 6.25 MiB is refused). At gzip's ~4.3x on an
// .nzb that is about a 25 MiB .nzb, a ~180 GB release. zstd would add ~9%
// in Go (measured 2026-09-30), not worth a second codec.
const MaxDownloadWireBytes = 23 << 18

// downloadCompressAbove is the body size ForWire starts compressing at: a
// .torrent or a small .nzb is sent as it is.
const downloadCompressAbove = 1 << 20

// ErrDownloadPayloadTooLarge is a payload over a size limit, on the wire or
// once decompressed.
var ErrDownloadPayloadTooLarge = errors.New("schema: download payload too large")

// ForWire is r as rpc.indexarr.download replies with it: a body over
// downloadCompressAbove gzipped, since an .nzb is XML that compresses about
// four times and a large 1080p encode's is over the inline budget as it is
// (The Godfather, 2026-09-30); and a body still over MaxDownloadWireBytes
// refused as an Error, which the broker would otherwise drop.
func (r DownloadResponse) ForWire() DownloadResponse {
	if r.Encoding != "" || len(r.Bytes) <= downloadCompressAbove {
		return r.fitsWire()
	}
	var z bytes.Buffer
	w := gzip.NewWriter(&z)
	if _, err := w.Write(r.Bytes); err != nil {
		return DownloadResponse{Error: fmt.Sprintf("indexarr: compress payload: %v", err)}
	}
	if err := w.Close(); err != nil {
		return DownloadResponse{Error: fmt.Sprintf("indexarr: compress payload: %v", err)}
	}
	r.Bytes, r.Encoding = z.Bytes(), DownloadEncodingGzip
	return r.fitsWire()
}

func (r DownloadResponse) fitsWire() DownloadResponse {
	if len(r.Bytes) > MaxDownloadWireBytes {
		return DownloadResponse{Error: fmt.Sprintf("indexarr: %v: %d bytes on the wire, over %d",
			ErrDownloadPayloadTooLarge, len(r.Bytes), MaxDownloadWireBytes)}
	}
	return r
}

// Payload is the .torrent or .nzb in r, decompressed as Encoding says,
// refusing more than maxBytes of it (a gzip bomb stops at the cap).
func (r DownloadResponse) Payload(maxBytes int64) ([]byte, error) {
	switch r.Encoding {
	case "":
		if int64(len(r.Bytes)) > maxBytes {
			return nil, fmt.Errorf("%w: %d bytes, over %d", ErrDownloadPayloadTooLarge, len(r.Bytes), maxBytes)
		}
		return r.Bytes, nil
	case DownloadEncodingGzip:
		zr, err := gzip.NewReader(bytes.NewReader(r.Bytes))
		if err != nil {
			return nil, fmt.Errorf("schema: gzip payload: %w", err)
		}
		defer func() { _ = zr.Close() }()
		b, err := io.ReadAll(io.LimitReader(zr, maxBytes+1))
		if err != nil {
			return nil, fmt.Errorf("schema: gzip payload: %w", err)
		}
		if int64(len(b)) > maxBytes {
			return nil, fmt.Errorf("%w: over %d bytes decompressed", ErrDownloadPayloadTooLarge, maxBytes)
		}
		return b, nil
	default:
		return nil, fmt.Errorf("schema: download payload encoding %q is not supported", r.Encoding)
	}
}
