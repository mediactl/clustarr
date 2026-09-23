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

package metadata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// MaxResponseBytes bounds how much of one provider response a metadata
// client buffers into memory (CLAUDE.md: "Every HTTP response body is read
// through a cap"). Without it a broken or hostile provider -- or a
// misconfigured MetadataProvider.spec.baseURL pointing at something that is
// not a metadata API at all -- could exhaust the gateway's memory, because
// every client used to decode straight off the socket. 8 MiB matches
// pkg/torznab and pkg/cardigann, and is roughly three times the largest
// real response this package asks for: a MusicBrainz release browse with
// inc=recordings, which MusicBrainz itself bounds at 500 tracks per page.
const MaxResponseBytes int64 = 8 << 20

// ReadBody reads r through a cap of max bytes. It reads one byte past max,
// so a body of exactly max bytes is accepted while anything larger is
// detected without ever buffering more than max+1 bytes, and reports that
// as ErrResponseTooLarge.
func ReadBody(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrResponseTooLarge, max)
	}
	return b, nil
}

// DecodeJSON reads r through ReadBody and unmarshals the result into out.
// A body over max is ErrResponseTooLarge; a body that is not valid JSON for
// out is ErrDecode -- the two are distinct so a caller never mistakes an
// oversized response for a malformed one.
func DecodeJSON(r io.Reader, max int64, out any) error {
	b, err := ReadBody(r, max)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%w: %w", ErrDecode, err)
	}
	return nil
}

// CappedTransport wraps base (http.DefaultTransport when nil) so that every
// response body is buffered through ReadBody before the caller sees it. A
// body over max fails the round trip itself with ErrResponseTooLarge, which
// net/http wraps in a *url.Error that still satisfies errors.Is.
//
// This is how a client built on a third-party library enforces the cap: the
// library owns the body read (golang-tmdb decodes straight off the socket,
// resty reads it whole), so the only place a cap can be applied is beneath
// it, in the transport. An in-house client reading its own bodies should
// call DecodeJSON instead, which needs no extra buffering.
func CappedTransport(base http.RoundTripper, max int64) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &cappedTransport{base: base, max: max}
}

type cappedTransport struct {
	base http.RoundTripper
	max  int64
}

func (t *cappedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	body, err := ReadBody(resp.Body, t.max)
	_ = resp.Body.Close()
	if err != nil {
		// http.RoundTripper forbids returning a response alongside an
		// error; the body is already closed above.
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return resp, nil
}
