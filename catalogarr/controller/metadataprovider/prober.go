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
	"errors"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/time/rate"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/audnexus"
	"github.com/mediactl/clustarr/pkg/metadata/clients/comicvine"
	"github.com/mediactl/clustarr/pkg/metadata/clients/musicbrainz"
	"github.com/mediactl/clustarr/pkg/metadata/clients/openlibrary"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
)

// ErrProviderNotImplemented is returned by NewProber and addToRegistry for a
// MetadataProviderType this package has no client for. Every value the
// CRD's enum admits has one since task X6b (the eight Phase B left without
// -- coverart, fanart, hardcover, metron, mangadex, anilist, kitsu,
// animelists -- are built by registry.go's buildSupplementary), so an
// admitted object no longer reaches it; it stays for a type added to the
// enum ahead of its client, which the Reconciler reports as
// Ready=Unknown/ProviderNotImplemented rather than an error.
var ErrProviderNotImplemented = errors.New("metadataprovider: no client exists for this provider type")

// ProbeResult carries whatever the probe learned that is worth writing to
// status beyond reachability itself.
type ProbeResult struct {
	QuotaRemaining *int32
}

// Prober checks that a configured provider is reachable and, where the
// provider takes credentials, that they were accepted. It performs exactly
// one cheap, read-only call -- a search or a zero-argument "what changed"
// call where the client offers one, never a call keyed by a guessed real
// entity id.
type Prober interface {
	Probe(ctx context.Context) (ProbeResult, error)
}

func baseURL(spec catalogv1alpha1.MetadataProviderSpec) string {
	if spec.BaseURL != nil {
		return *spec.BaseURL
	}
	return "" // every client in pkg/metadata/clients defaults its own baseURL on ""
}

func limiterFor(spec catalogv1alpha1.MetadataProviderSpec, def rate.Limit, defBurst int) *rate.Limiter {
	if spec.RateLimit == nil {
		return metadata.NewLimiter(def, defBurst)
	}
	rps := def
	if spec.RateLimit.RequestsPerSecond != nil {
		rps = rate.Limit(spec.RateLimit.RequestsPerSecond.AsApproximateFloat64())
	}
	burst := defBurst
	if spec.RateLimit.Burst > 0 {
		burst = int(spec.RateLimit.Burst)
	}
	return metadata.NewLimiter(rps, burst)
}

// NewProber builds the Prober for spec.Type. The six Phase B types have
// their own probers below; the eight task X6b added probe through their
// client's Ping (newSupplementaryProber, registry.go); any other type is
// ErrProviderNotImplemented.
func NewProber(spec catalogv1alpha1.MetadataProviderSpec, secret map[string][]byte, httpClient *http.Client) (Prober, error) {
	limits := metadata.DefaultLimits()

	switch spec.Type {
	case catalogv1alpha1.MetadataProviderTMDB:
		apiKey := string(secret["apiKey"])
		if apiKey == "" {
			return nil, fmt.Errorf("metadataprovider: tmdb requires secretRef key apiKey")
		}
		c, err := tmdb.New(apiKey, httpClient, baseURL(spec), limiterFor(spec, limits.TMDB, limits.TMDBBurst))
		if err != nil {
			return nil, fmt.Errorf("metadataprovider: build tmdb client: %w", err)
		}
		return tmdbProber{c}, nil

	case catalogv1alpha1.MetadataProviderTVDB:
		apiKey, pin := string(secret["apiKey"]), string(secret["pin"])
		if apiKey == "" {
			return nil, fmt.Errorf("metadataprovider: tvdb requires secretRef key apiKey")
		}
		c := tvdb.New(apiKey, pin, httpClient, baseURL(spec), limiterFor(spec, limits.TVDB, limits.TVDBBurst))
		return tvdbProber{c}, nil

	case catalogv1alpha1.MetadataProviderMusicBrainz:
		c, err := musicbrainz.New(spec.ContactUserAgent, httpClient, baseURL(spec), limiterFor(spec, limits.MusicBrainz, limits.MusicBrainzBurst))
		if err != nil {
			return nil, fmt.Errorf("metadataprovider: build musicbrainz client: %w", err)
		}
		return musicbrainzProber{c}, nil

	case catalogv1alpha1.MetadataProviderOpenLibrary:
		if spec.ContactUserAgent == "" {
			return nil, fmt.Errorf("metadataprovider: openlibrary requires contactUserAgent")
		}
		c := openlibrary.New(spec.ContactUserAgent, httpClient, baseURL(spec), limiterFor(spec, limits.OpenLibrary, limits.OpenLibraryBurst))
		return openlibraryProber{c}, nil

	case catalogv1alpha1.MetadataProviderComicVine:
		apiKey := string(secret["apiKey"])
		if apiKey == "" {
			return nil, fmt.Errorf("metadataprovider: comicvine requires secretRef key apiKey")
		}
		c := comicvine.New(apiKey, httpClient, baseURL(spec), limiterFor(spec, limits.ComicVine, limits.ComicVineBurst))
		return comicvineProber{c}, nil

	case catalogv1alpha1.MetadataProviderAudnexus:
		c := audnexus.New(httpClient, baseURL(spec), limiterFor(spec, limits.Audnexus, limits.AudnexusBurst))
		return audnexusProber{c}, nil

	default:
		return newSupplementaryProber(spec, secret, httpClient)
	}
}

type tmdbProber struct{ c *tmdb.Client }

// probeTMDBMovieID is The Matrix (1999), TMDB id 603 -- one of the platform's
// oldest and most stable catalog entries, chosen only for being a permanent,
// well-formed id. SearchMovies would be the more natural probe (a search
// proves reachability without asserting any particular title exists), but
// pkg/metadata/clients/tmdb.Client.SearchMovies is an unimplemented stub
// that always returns metadata.ErrUnsupported without making a call (Phase
// B shipped it only to satisfy the metadata.MovieProvider interface; see
// its doc comment "not implemented by this task ... until a later task
// needs it") -- it cannot serve as a reachability/credential probe at all,
// so Movie() is used instead, the same "fixed, stable input" pattern
// audnexusProber below already uses for the same reason.
const probeTMDBMovieID = "603"

func (p tmdbProber) Probe(ctx context.Context) (ProbeResult, error) {
	_, err := p.c.Movie(ctx, probeTMDBMovieID, "")
	return ProbeResult{}, err
}

type openlibraryProber struct{ c *openlibrary.Client }

func (p openlibraryProber) Probe(ctx context.Context) (ProbeResult, error) {
	_, err := p.c.SearchBooks(ctx, "the lord of the rings")
	return ProbeResult{}, err
}

type tvdbProber struct{ c *tvdb.Client }

func (p tvdbProber) Probe(ctx context.Context) (ProbeResult, error) {
	// Updates(since) takes no id at all -- the cleanest possible reachability
	// probe, since there is nothing to get wrong about what it asks for.
	_, err := p.c.Updates(ctx, time.Now().Add(-1*time.Hour))
	return ProbeResult{}, err
}

type musicbrainzProber struct{ c *musicbrainz.Client }

// probeMBArtistID is Radiohead's real, permanent MusicBrainz artist MBID.
// SearchArtists would be the more natural probe, but
// pkg/metadata/clients/musicbrainz.Client.SearchArtists is an unimplemented
// stub that always returns metadata.ErrUnsupported without making a call
// ("MusicBrainz search uses Lucene query syntax, which is out of scope
// here" per its doc comment) -- it cannot serve as a reachability probe, so
// Artist() is used instead with a fixed, stable id, the same pattern
// tmdbProber and audnexusProber use for the same reason.
const probeMBArtistID = "a74b1b7f-71a5-4011-9441-d0b5e4122711"

func (p musicbrainzProber) Probe(ctx context.Context) (ProbeResult, error) {
	_, err := p.c.Artist(ctx, probeMBArtistID)
	return ProbeResult{}, err
}

type comicvineProber struct{ c *comicvine.Client }

func (p comicvineProber) Probe(ctx context.Context) (ProbeResult, error) {
	_, err := p.c.SearchVolumes(ctx, "batman")
	return ProbeResult{}, err
}

type audnexusProber struct{ c *audnexus.Client }

func (p audnexusProber) Probe(ctx context.Context) (ProbeResult, error) {
	_, err := p.c.Chapters(ctx, "B08G9PRS1K", "us")
	return ProbeResult{}, err
}
