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

package tmdbstub_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/metadataprovider"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
	"github.com/mediactl/clustarr/test/fixtures/tmdbstub"
)

func newStub(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(tmdbstub.NewHandler("../../data/metadata/tmdb", slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The tmdb MetadataProvider's real health probe -- the prober the manager's
// MetadataProvider controller builds from config/e2e's spec -- answers
// against the stub, so the provider reads Ready. It asked for /movie/603,
// which the stub did not serve, and config/e2e's tmdb provider read
// Ready=False (found by the smoke test, 2026-10-07).
func TestTheTMDBProvidersHealthProbeAnswers(t *testing.T) {
	base := newStub(t)
	p, err := metadataprovider.NewProber(catalogv1alpha1.MetadataProviderSpec{
		Type:    catalogv1alpha1.MetadataProviderTMDB,
		BaseURL: &base,
	}, map[string][]byte{"apiKey": []byte("e2e-fixture-key")}, nil)
	require.NoError(t, err)
	_, err = p.Probe(context.Background())
	require.NoError(t, err, "the tmdb provider's health probe fails against the stub")
}

// Every movie the stub serves decodes through the real client as the movie
// it names: the probe's The Matrix in the recorded shape, and the recorded
// Inception every e2e scenario adds.
func TestTheStubsMoviesDecodeThroughTheRealClient(t *testing.T) {
	c, err := tmdb.New("e2e-fixture-key", nil, newStub(t), metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)
	for id, want := range map[string]struct {
		title string
		year  int32
		imdb  string
	}{
		tmdbstub.ProbeMovieID: {"The Matrix", 1999, "tt0133093"},
		"27205":               {"Inception", 2010, "tt1375666"},
	} {
		m, err := c.Movie(context.Background(), id, "")
		require.NoError(t, err, id)
		assert.Equal(t, want.title, m.Title, id)
		assert.Equal(t, want.year, m.Year, id)
		assert.Equal(t, want.imdb, m.IDs[metadata.KeyIMDb], id)
	}
}
