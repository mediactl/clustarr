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

package metadataprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
)

func TestTMDBProberSucceeds(t *testing.T) {
	// tmdbProber.Probe calls Movie(ctx, "603", "") -- see prober.go's
	// probeTMDBMovieID comment for why (SearchMovies is an unimplemented
	// Phase B stub) -- so the fake server answers a movie-details shape,
	// not a search-results shape.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 603, "title": "The Matrix"})
	}))
	defer srv.Close()

	p, err := NewProber(
		catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderTMDB, BaseURL: &srv.URL},
		map[string][]byte{"apiKey": []byte("test-key")},
		srv.Client(),
	)
	if err != nil {
		t.Fatalf("NewProber: %v", err)
	}
	if _, err := p.Probe(context.Background()); err != nil {
		t.Errorf("Probe: %v", err)
	}
}

func TestTMDBProberMissingAPIKeyFailsFast(t *testing.T) {
	if _, err := NewProber(
		catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderTMDB},
		nil, http.DefaultClient,
	); err == nil {
		t.Error("no apiKey secret data accepted for tmdb")
	}
}

func TestOpenLibraryProberSucceedsWithNoCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"docs": []any{}, "numFound": 0})
	}))
	defer srv.Close()

	p, err := NewProber(
		catalogv1alpha1.MetadataProviderSpec{
			Type:             catalogv1alpha1.MetadataProviderOpenLibrary,
			BaseURL:          &srv.URL,
			ContactUserAgent: "clustarr-test/0.0 (test@example.invalid)",
		},
		nil, srv.Client(),
	)
	if err != nil {
		t.Fatalf("NewProber: %v", err)
	}
	if _, err := p.Probe(context.Background()); err != nil {
		t.Errorf("Probe: %v", err)
	}
}

func TestUnimplementedProviderTypeIsExplicit(t *testing.T) {
	_, err := NewProber(catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderCoverArt}, nil, http.DefaultClient)
	if err != ErrProviderNotImplemented {
		t.Errorf("err = %v, want ErrProviderNotImplemented", err)
	}
}

func TestTMDBProberMapsAuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 7, "status_message": "Invalid API key"})
	}))
	defer srv.Close()

	p, err := NewProber(
		catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderTMDB, BaseURL: &srv.URL},
		map[string][]byte{"apiKey": []byte("bad-key")},
		srv.Client(),
	)
	if err != nil {
		t.Fatalf("NewProber: %v", err)
	}
	// pkg/metadata does not expose an IsAuthError helper (verified with `go
	// doc ./pkg/metadata`), so this asserts against the sentinel directly,
	// the way tmdb.Client.mapError itself returns it (bare, not wrapped).
	if _, err := p.Probe(context.Background()); !errors.Is(err, metadata.ErrAuth) {
		t.Errorf("Probe err = %v, want an error wrapping metadata.ErrAuth", err)
	}
}

func TestTVDBProberSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"token": "fake-jwt"}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		}
	}))
	defer srv.Close()

	p, err := NewProber(
		catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderTVDB, BaseURL: &srv.URL},
		map[string][]byte{"apiKey": []byte("k"), "pin": []byte("0000")},
		srv.Client(),
	)
	if err != nil {
		t.Fatalf("NewProber: %v", err)
	}
	if _, err := p.Probe(context.Background()); err != nil {
		t.Errorf("Probe: %v", err)
	}
}

func TestMusicBrainzProberSucceeds(t *testing.T) {
	// musicbrainzProber.Probe calls Artist(ctx, probeMBArtistID) -- see
	// prober.go's comment for why (SearchArtists is an unimplemented Phase B
	// stub) -- so the fake server answers a single-artist shape, not a
	// search-results shape.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "a74b1b7f-71a5-4011-9441-d0b5e4122711", "name": "Radiohead",
		})
	}))
	defer srv.Close()

	p, err := NewProber(
		catalogv1alpha1.MetadataProviderSpec{
			Type: catalogv1alpha1.MetadataProviderMusicBrainz, BaseURL: &srv.URL,
			ContactUserAgent: "clustarr-test/0.0 (test@example.invalid)",
		},
		nil, srv.Client(),
	)
	if err != nil {
		t.Fatalf("NewProber: %v", err)
	}
	if _, err := p.Probe(context.Background()); err != nil {
		t.Errorf("Probe: %v", err)
	}
}

func TestComicVineProberSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 1, "results": []any{}})
	}))
	defer srv.Close()

	p, err := NewProber(
		catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderComicVine, BaseURL: &srv.URL},
		map[string][]byte{"apiKey": []byte("k")},
		srv.Client(),
	)
	if err != nil {
		t.Fatalf("NewProber: %v", err)
	}
	if _, err := p.Probe(context.Background()); err != nil {
		t.Errorf("Probe: %v", err)
	}
}

func TestAudnexusProberSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"chapters": []any{}, "runtimeLengthMs": 0})
	}))
	defer srv.Close()

	p, err := NewProber(
		catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderAudnexus, BaseURL: &srv.URL},
		nil, srv.Client(),
	)
	if err != nil {
		t.Fatalf("NewProber: %v", err)
	}
	// B08G9PRS1K (Project Hail Mary, an Audible catalog entry, chosen only
	// for being a stable, syntactically valid ASIN) -- the fake server above
	// answers any path, so the specific value only has to be well-formed.
	if _, err := p.Probe(context.Background()); err != nil {
		t.Errorf("Probe: %v", err)
	}
}

// isAuthError/isRateLimited unit tests (TestIsAuthErrorMatchesWrappedErrAuth,
// TestIsRateLimitedMatchesRateLimitedError) are added alongside controller.go
// in a later step, since those two helpers live there.
