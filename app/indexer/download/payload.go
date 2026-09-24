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
	"errors"
	"fmt"
	"io"
	"mime"
)

// MaxPayloadBytes bounds a proxied .torrent/.nzb body. See
// TestMaxPayloadFitsOneNATSMessage for the arithmetic: base64 expands a body
// by 4/3, the whole DownloadResponse is one NATS message, and the broker's
// max_payload is 8Mi (config/nats/configmap.yaml:20). 4 MiB encodes to about
// 5.34 MiB and leaves room for the envelope, while sitting far above any real
// .torrent and above all but pathological .nzb files.
//
// This is deliberately NOT pkg/torznab's maxResponseBodyBytes (8 MiB): that
// cap bounds an XML document that is parsed and discarded, this one bounds
// bytes that must survive a round trip through the broker.
const MaxPayloadBytes = 4 << 20

// ErrResponseTooLarge follows the pkg/torznab convention (client.go:176): a
// package-level max, an io.LimitReader(body, max+1) and a sentinel, so
// errors.Is works through the wrapping. pkg/metadata/clients reads bodies
// with no cap at all; that is a recorded defect, not a second convention.
var ErrResponseTooLarge = errors.New("indexarr/download: response body exceeds size limit")

// errEmptyBody is a 200 with nothing in it. A torrent client handed zero
// bytes reports a corrupt file, which reads as our bug rather than the
// indexer's.
var errEmptyBody = errors.New("indexarr/download: indexer returned an empty body")

// maxContentTypeChars bounds a header value from a third party before it is
// copied into DownloadResponse.ContentType.
const maxContentTypeChars = 128

// readPayload reads r through the cap. One byte past the limit is read so a
// body exactly at the limit is accepted while anything larger is detected
// without ever buffering more than MaxPayloadBytes+1.
func readPayload(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxPayloadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxPayloadBytes {
		return nil, fmt.Errorf("%w: at least %d bytes", ErrResponseTooLarge, len(b))
	}
	if len(b) == 0 {
		return nil, errEmptyBody
	}
	return b, nil
}

// payloadKind is what the first bytes of a body say it is.
type payloadKind int

const (
	kindUnknown payloadKind = iota
	kindTorrent
	kindNZB
	kindHTML
)

// sniffKind classifies a body by its first bytes. The HTML case is the one
// that earns this function: a tracker whose session has expired answers a
// download link with a login page and HTTP 200, and without this check the
// login page reaches a torrent client as a "torrent".
func sniffKind(b []byte) payloadKind {
	t := bytes.TrimLeft(b, " \t\r\n")
	switch {
	case len(t) == 0:
		return kindUnknown
	case t[0] == 'd': // bencode dictionary
		return kindTorrent
	case hasPrefixFold(t, []byte("<!doctype html")), hasPrefixFold(t, []byte("<html")):
		return kindHTML
	case hasPrefixFold(t, []byte("<?xml")), hasPrefixFold(t, []byte("<nzb")):
		return kindNZB
	default:
		return kindUnknown
	}
}

func hasPrefixFold(b, prefix []byte) bool {
	return len(b) >= len(prefix) && bytes.EqualFold(b[:len(prefix)], prefix)
}

// contentTypeFor prefers what the bytes ARE over what the indexer claimed.
// An unrecognised body falls back to the header, parsed by mime.ParseMediaType
// so parameters are dropped, and bounded -- the header is a third-party string
// on its way into another controller's object.
func contentTypeFor(header string, k payloadKind) string {
	switch k {
	case kindTorrent:
		return "application/x-bittorrent"
	case kindNZB:
		return "application/x-nzb"
	case kindUnknown, kindHTML:
	}
	if mt, _, err := mime.ParseMediaType(header); err == nil && mt != "" {
		return truncate(mt, maxContentTypeChars)
	}
	return "application/octet-stream"
}
