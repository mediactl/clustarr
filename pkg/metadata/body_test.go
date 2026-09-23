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

package metadata_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata"
)

func TestReadBodyAcceptsExactlyTheCapAndRejectsOneByteMore(t *testing.T) {
	got, err := metadata.ReadBody(strings.NewReader("12345"), 5)
	require.NoError(t, err)
	require.Equal(t, "12345", string(got))

	_, err = metadata.ReadBody(strings.NewReader("123456"), 5)
	require.ErrorIs(t, err, metadata.ErrResponseTooLarge)
}

func TestDecodeJSONDistinguishesTooLargeFromMalformed(t *testing.T) {
	var out map[string]any

	err := metadata.DecodeJSON(strings.NewReader(`{"a":1}`), 64, &out)
	require.NoError(t, err)
	require.EqualValues(t, 1, out["a"])

	err = metadata.DecodeJSON(strings.NewReader(`{"a":`), 64, &out)
	require.ErrorIs(t, err, metadata.ErrDecode)
	require.NotErrorIs(t, err, metadata.ErrResponseTooLarge)

	err = metadata.DecodeJSON(strings.NewReader(`{"a":"`+strings.Repeat("x", 64)+`"}`), 64, &out)
	require.ErrorIs(t, err, metadata.ErrResponseTooLarge)
	require.NotErrorIs(t, err, metadata.ErrDecode, "an oversized body is never parsed, so it is not a decode failure")
}

func TestCappedTransportFailsTheRoundTripOnAnOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big" {
			_, _ = w.Write(bytes.Repeat([]byte("x"), 1025))
			return
		}
		_, _ = w.Write(bytes.Repeat([]byte("y"), 1024))
	}))
	defer srv.Close()
	hc := &http.Client{Transport: metadata.CappedTransport(srv.Client().Transport, 1024)}

	resp, err := hc.Get(srv.URL + "/ok")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Len(t, body, 1024, "a body at exactly the cap is delivered intact")
	require.EqualValues(t, 1024, resp.ContentLength)

	_, err = hc.Get(srv.URL + "/big") //nolint:bodyclose // the round trip fails, there is no body
	require.ErrorIs(t, err, metadata.ErrResponseTooLarge, "net/http's *url.Error must still unwrap to the sentinel")
}
