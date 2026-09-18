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

// TestEngineErrorsDoNotLeakQuerySecrets covers ruling F6. Definition inputs
// are rendered from .Config.<setting> and SettingsField.Type supports
// "password", so a real definition puts apikey/passkey/rsskey in the query
// string. Those errors end up in Indexer.status and in logs, so no error
// this package returns may carry a query string — while still naming the
// host and path, which is what makes the error diagnosable.
func TestEngineErrorsDoNotLeakQuerySecrets(t *testing.T) {
	const secret = "SUPERSECRETPASSKEY"

	t.Run("transport error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		base := srv.URL
		srv.Close() // nothing is listening: Do fails with a *url.Error carrying the full URL

		def := &cardigann.Definition{Links: []string{base + "/"}}
		cfg, err := cardigann.NewConfig(def, base+"/", nil)
		require.NoError(t, err)

		_, err = cardigann.Engine{}.Download(context.Background(), def, cfg, "/dl.php?id=42&apikey="+secret)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
		assert.NotContains(t, err.Error(), "apikey")
		assert.Contains(t, err.Error(), "/dl.php", "the path must survive redaction")
	})

	t.Run("oversized body error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(bytes.Repeat([]byte("a"), maxResponseBodyBytesForTest+1))
		}))
		defer srv.Close()

		def := &cardigann.Definition{Links: []string{srv.URL + "/"}}
		cfg, err := cardigann.NewConfig(def, srv.URL+"/", nil)
		require.NoError(t, err)

		_, err = cardigann.Engine{HTTP: srv.Client()}.Download(context.Background(), def, cfg, "/dl.php?passkey="+secret)
		require.Error(t, err)
		require.ErrorIs(t, err, cardigann.ErrResponseTooLarge)
		assert.NotContains(t, err.Error(), secret)
		assert.Contains(t, err.Error(), "/dl.php")
	})

	t.Run("unparseable path", func(t *testing.T) {
		// A path url.Parse rejects (a control character) never reaches the
		// network, so this covers resolveURL's own error, which used to
		// interpolate the whole rendered path -- query string and all.
		def := &cardigann.Definition{Links: []string{"http://tracker.test/"}}
		cfg, err := cardigann.NewConfig(def, "http://tracker.test/", nil)
		require.NoError(t, err)

		_, err = cardigann.Engine{}.Download(context.Background(), def, cfg, "/dl.php?apikey="+secret+"\x7f")
		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
		assert.Contains(t, err.Error(), "/dl.php")
	})
}
