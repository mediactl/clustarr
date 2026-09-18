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

package cardigann_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/cardigann"
)

// maxResponseBodyBytesForTest mirrors pkg/cardigann's own unexported
// maxResponseBodyBytes (8 MiB, matching pkg/torznab.Client's cap) — kept
// as a local literal since the constant itself is intentionally
// unexported, same as torznab's.
const maxResponseBodyBytesForTest = 8 << 20

func TestEngineDoRejectsOversizedResponse(t *testing.T) {
	oversized := bytes.Repeat([]byte("a"), maxResponseBodyBytesForTest+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(oversized)
	}))
	defer srv.Close()

	def := &cardigann.Definition{Links: []string{srv.URL + "/"}}
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", nil)
	require.NoError(t, err)
	eng := cardigann.Engine{HTTP: srv.Client()}

	// Download with no DownloadBlock fetches link directly through
	// Engine's shared do() helper — the same low-level path every login,
	// search and download request goes through, so this one assertion
	// covers all of them.
	_, err = eng.Download(context.Background(), def, cfg, "/file")
	require.Error(t, err)
	assert.True(t, errors.Is(err, cardigann.ErrResponseTooLarge))
}

func TestEngineDoAcceptsResponseAtExactlyTheSizeLimit(t *testing.T) {
	atLimit := bytes.Repeat([]byte("a"), maxResponseBodyBytesForTest)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(atLimit)
	}))
	defer srv.Close()

	def := &cardigann.Definition{Links: []string{srv.URL + "/"}}
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", nil)
	require.NoError(t, err)
	eng := cardigann.Engine{HTTP: srv.Client()}

	rc, err := eng.Download(context.Background(), def, cfg, "/file")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Len(t, body, maxResponseBodyBytesForTest)
}
