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
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/anilist"
	"github.com/mediactl/clustarr/pkg/metadata/clients/animelists"
	"github.com/mediactl/clustarr/pkg/metadata/clients/audnexus"
	"github.com/mediactl/clustarr/pkg/metadata/clients/comicvine"
	"github.com/mediactl/clustarr/pkg/metadata/clients/coverart"
	"github.com/mediactl/clustarr/pkg/metadata/clients/fanart"
	"github.com/mediactl/clustarr/pkg/metadata/clients/hardcover"
	"github.com/mediactl/clustarr/pkg/metadata/clients/kitsu"
	"github.com/mediactl/clustarr/pkg/metadata/clients/mangadex"
	"github.com/mediactl/clustarr/pkg/metadata/clients/metron"
	"github.com/mediactl/clustarr/pkg/metadata/clients/musicbrainz"
	"github.com/mediactl/clustarr/pkg/metadata/clients/openlibrary"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// BuildRegistry constructs a pkg/metadata.Registry from the cluster's
// MetadataProvider objects, in ascending spec.priority order (lower wins,
// matching Registry.Lookup's "first in the slice, first tried"). All
// fourteen MetadataProviderType values are wired; an enabled provider of a
// type this switch does not know is skipped, not an error, so an operator
// can create the CR ahead of a client landing.
//
// A priority tie is broken in favour of a primary provider -- one whose ids
// a catalog kind's CR is keyed by -- over a supplementary one
// (isSupplementary), then by the list's own order. The Registry's search
// and lookup take the first provider that succeeds, and every
// MetadataProvider defaults to priority 50, so without the tie-break a
// default-priority Hardcover would answer book searches ahead of Open
// Library -- alphabetically first -- with hits a Book CR, keyed by an Open
// Library work, cannot hold.
func BuildRegistry(ctx context.Context, c client.Client, providers []catalogv1alpha1.MetadataProvider, httpClient *http.Client) (*pkgmetadata.Registry, error) {
	sorted := make([]catalogv1alpha1.MetadataProvider, len(providers))
	copy(sorted, providers)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Spec.Priority != sorted[j].Spec.Priority {
			return sorted[i].Spec.Priority < sorted[j].Spec.Priority
		}
		return !isSupplementary(sorted[i].Spec.Type) && isSupplementary(sorted[j].Spec.Type)
	})

	reg := &pkgmetadata.Registry{}
	for _, p := range sorted {
		if p.Spec.Enabled != nil && !*p.Spec.Enabled {
			continue
		}
		if err := addProvider(ctx, c, reg, p, httpClient); err != nil {
			if errors.Is(err, ErrProviderCredentials) {
				// The controller reports this one Ready=False InvalidSpec; it
				// must not take the others down with the gateway (a hardcover
				// provider against a Secret holding apiKey where hardcover
				// reads bearer crash-looped catalogarr-metadata on kind,
				// 2026-09-24).
				logging.FromContext(ctx).Error("metadata provider skipped: its credentials cannot be read",
					"namespace", p.Namespace, "provider", p.Name, "type", p.Spec.Type, "error", err)
				continue
			}
			return nil, err
		}
	}
	return reg, nil
}

// addProvider wires one enabled provider into reg, or returns why it
// cannot be: an [ErrProviderCredentials] the caller skips, anything else
// fatal.
func addProvider(ctx context.Context, c client.Client, reg *pkgmetadata.Registry, p catalogv1alpha1.MetadataProvider, httpClient *http.Client) error {
	limiter := resolveLimiter(p.Spec.Type, p.Spec.RateLimit)
	{
		switch p.Spec.Type {
		case catalogv1alpha1.MetadataProviderTMDB:
			key, err := secretValue(ctx, c, p, catalogv1alpha1.MetadataSecretKeyAPIKey)
			if err != nil {
				return err
			}
			cl, err := tmdb.New(key, httpClient, baseURL(p, "https://api.themoviedb.org/3"), limiter)
			if err != nil {
				return fmt.Errorf("metadata: build tmdb client for %s/%s: %w", p.Namespace, p.Name, err)
			}
			reg.Movies = append(reg.Movies, cl)
			reg.Ratings = append(reg.Ratings, cl) // spec §C.2: tmdb declares its own source from the fetch it already performs.
		case catalogv1alpha1.MetadataProviderTVDB:
			key, err := secretValue(ctx, c, p, catalogv1alpha1.MetadataSecretKeyAPIKey)
			if err != nil {
				return err
			}
			pin, _ := secretValue(ctx, c, p, catalogv1alpha1.MetadataSecretKeyPin)
			reg.Series = append(reg.Series, tvdb.New(key, pin, httpClient, baseURL(p, "https://api4.thetvdb.com/v4"), limiter))
		case catalogv1alpha1.MetadataProviderMusicBrainz:
			cl, err := musicbrainz.New(p.Spec.ContactUserAgent, httpClient, baseURL(p, "https://musicbrainz.org/ws/2"), limiter)
			if err != nil {
				return fmt.Errorf("metadata: build musicbrainz client for %s/%s: %w", p.Namespace, p.Name, err)
			}
			reg.Artists = append(reg.Artists, cl)
		case catalogv1alpha1.MetadataProviderOpenLibrary:
			reg.Books = append(reg.Books, openlibrary.New(p.Spec.ContactUserAgent, httpClient, baseURL(p, "https://openlibrary.org"), limiter))
		case catalogv1alpha1.MetadataProviderAudnexus:
			reg.Audiobooks = append(reg.Audiobooks, audnexus.New(httpClient, baseURL(p, "https://api.audnex.us"), limiter))
		case catalogv1alpha1.MetadataProviderComicVine:
			key, err := secretValue(ctx, c, p, catalogv1alpha1.MetadataSecretKeyAPIKey)
			if err != nil {
				return err
			}
			reg.Comics = append(reg.Comics, comicvine.New(key, httpClient, baseURL(p, "https://comicvine.gamespot.com/api"), limiter))
		default:
			return addSupplementary(ctx, c, reg, p, httpClient)
		}
	}
	return nil
}

// isSupplementary reports whether t is a provider no catalog CR is keyed
// by: artwork-only (coverart, fanart), a secondary source for a kind whose
// CR carries another provider's id (hardcover for Open Library-keyed books,
// metron for ComicVine-keyed comics), ratings-only (mdblist, omdb -- no
// catalog CR is ever keyed by either), or a pure crosswalk (anilist, kitsu,
// animelists). mangadex is primary: a Comic can name it as its source.
//
// mdblist and omdb have no case in addSupplementary below: ruling R5 (spec
// §C.3) blocks writing either client without a recorded response shape,
// with neither MDBLIST_API_KEY nor OMDB_API_KEY set as of task C1. A
// MetadataProvider of either type therefore falls through addSupplementary
// silently -- no client, no error, matching every other type this switch
// does not yet know -- and is included here only so its priority-tie
// position is correct once C1's follow-up adds the case. The CR itself
// still reports NotReady with an explicit reason: see the sibling
// buildSupplementary in app/catalog/controller/metadataprovider/registry.go,
// which this package does not share code with but must stay consistent
// with (CLAUDE.md: "grep for the other copies of the thing you fixed").
func isSupplementary(t catalogv1alpha1.MetadataProviderType) bool {
	switch t {
	case catalogv1alpha1.MetadataProviderCoverArt, catalogv1alpha1.MetadataProviderFanart,
		catalogv1alpha1.MetadataProviderHardcover, catalogv1alpha1.MetadataProviderMetron,
		catalogv1alpha1.MetadataProviderAniList, catalogv1alpha1.MetadataProviderKitsu,
		catalogv1alpha1.MetadataProviderAnimeLists,
		catalogv1alpha1.MetadataProviderMDBList, catalogv1alpha1.MetadataProviderOMDb:
		return true
	default:
		return false
	}
}

// addSupplementary wires the eight provider types that shipped without a
// client until task X6b, each into every Registry slot its client fills:
//
//   - coverart, fanart: Artwork.
//   - hardcover: Books.
//   - metron, mangadex: Comics and Resolvers.
//   - anilist, kitsu, animelists: Resolvers. AniList is a ComicProvider
//     too, but not registered as one: a Comic's source can only be
//     ComicVine or MangaDex, so AniList hits in a comic search would be
//     titles no Comic can be created from.
//
// Credentials: fanart reads secretRef's "apiKey"; hardcover and metron read
// "bearer" (both authenticate with a Bearer token). spec.contactUserAgent,
// when set, is every client's User-Agent. With no spec.rateLimit each
// client gets its package's documented (or, where the provider publishes
// none, chosen) DefaultRate and DefaultBurst rather than resolveLimiter's
// one-request-a-second placeholder.
func addSupplementary(ctx context.Context, c client.Client, reg *pkgmetadata.Registry, p catalogv1alpha1.MetadataProvider, httpClient *http.Client) error {
	ua := p.Spec.ContactUserAgent
	switch p.Spec.Type {
	case catalogv1alpha1.MetadataProviderCoverArt:
		reg.Artwork = append(reg.Artwork, coverart.New(coverart.Config{
			HTTPClient: httpClient, BaseURL: baseURL(p, coverart.DefaultBaseURL),
			Limiter: supplementaryLimiter(p, coverart.DefaultRate, coverart.DefaultBurst), UserAgent: ua,
		}))
	case catalogv1alpha1.MetadataProviderFanart:
		key, err := secretValue(ctx, c, p, catalogv1alpha1.MetadataSecretKeyAPIKey)
		if err != nil {
			return err
		}
		cl, err := fanart.New(fanart.Config{
			HTTPClient: httpClient, BaseURL: baseURL(p, fanart.DefaultBaseURL),
			Limiter: supplementaryLimiter(p, fanart.DefaultRate, fanart.DefaultBurst), UserAgent: ua, APIKey: key,
		})
		if err != nil {
			return fmt.Errorf("metadata: build fanart client for %s/%s: %w", p.Namespace, p.Name, err)
		}
		reg.Artwork = append(reg.Artwork, cl)
	case catalogv1alpha1.MetadataProviderHardcover:
		tok, err := secretValue(ctx, c, p, catalogv1alpha1.MetadataSecretKeyBearer)
		if err != nil {
			return err
		}
		cl, err := hardcover.New(hardcover.Config{
			HTTPClient: httpClient, BaseURL: baseURL(p, hardcover.DefaultBaseURL),
			Limiter: supplementaryLimiter(p, hardcover.DefaultRate, hardcover.DefaultBurst), UserAgent: ua, Token: tok,
		})
		if err != nil {
			return fmt.Errorf("metadata: build hardcover client for %s/%s: %w", p.Namespace, p.Name, err)
		}
		reg.Books = append(reg.Books, cl)
	case catalogv1alpha1.MetadataProviderMetron:
		tok, err := secretValue(ctx, c, p, catalogv1alpha1.MetadataSecretKeyBearer)
		if err != nil {
			return err
		}
		cl, err := metron.New(metron.Config{
			HTTPClient: httpClient, BaseURL: baseURL(p, metron.DefaultBaseURL),
			Limiter: supplementaryLimiter(p, metron.DefaultRate, metron.DefaultBurst), UserAgent: ua, Token: tok,
		})
		if err != nil {
			return fmt.Errorf("metadata: build metron client for %s/%s: %w", p.Namespace, p.Name, err)
		}
		reg.Comics = append(reg.Comics, cl)
		reg.Resolvers = append(reg.Resolvers, cl)
	case catalogv1alpha1.MetadataProviderMangaDex:
		cl := mangadex.New(mangadex.Config{
			HTTPClient: httpClient, BaseURL: baseURL(p, mangadex.DefaultBaseURL),
			Limiter: supplementaryLimiter(p, mangadex.DefaultRate, mangadex.DefaultBurst), UserAgent: ua,
		})
		reg.Comics = append(reg.Comics, cl)
		reg.Resolvers = append(reg.Resolvers, cl)
	case catalogv1alpha1.MetadataProviderAniList:
		reg.Resolvers = append(reg.Resolvers, anilist.New(anilist.Config{
			HTTPClient: httpClient, BaseURL: baseURL(p, anilist.DefaultBaseURL),
			Limiter: supplementaryLimiter(p, anilist.DefaultRate, anilist.DefaultBurst), UserAgent: ua,
		}))
	case catalogv1alpha1.MetadataProviderKitsu:
		reg.Resolvers = append(reg.Resolvers, kitsu.New(kitsu.Config{
			HTTPClient: httpClient, BaseURL: baseURL(p, kitsu.DefaultBaseURL),
			Limiter: supplementaryLimiter(p, kitsu.DefaultRate, kitsu.DefaultBurst), UserAgent: ua,
		}))
	case catalogv1alpha1.MetadataProviderAnimeLists:
		reg.Resolvers = append(reg.Resolvers, animelists.New(animelists.Config{
			HTTPClient: httpClient, URL: baseURL(p, animelists.DefaultURL),
			Limiter: supplementaryLimiter(p, animelists.DefaultRate, animelists.DefaultBurst), UserAgent: ua,
		}))
	}
	return nil
}

// supplementaryLimiter is resolveLimiter when the provider sets a rate, and
// the client package's own default when it does not.
func supplementaryLimiter(p catalogv1alpha1.MetadataProvider, def rate.Limit, burst int) *rate.Limiter {
	if p.Spec.RateLimit != nil && p.Spec.RateLimit.RequestsPerSecond != nil {
		return resolveLimiter(p.Spec.Type, p.Spec.RateLimit)
	}
	return pkgmetadata.NewLimiter(def, burst)
}

func baseURL(p catalogv1alpha1.MetadataProvider, def string) string {
	if p.Spec.BaseURL != nil && *p.Spec.BaseURL != "" {
		return *p.Spec.BaseURL
	}
	return def
}

// ErrProviderCredentials is every way a provider's credentials cannot be
// read: no secretRef, a Secret that is not there, or one without the key
// its type reads. BuildRegistry skips such a provider rather than failing.
var ErrProviderCredentials = errors.New("metadata: provider credentials cannot be read")

func secretValue(ctx context.Context, c client.Client, p catalogv1alpha1.MetadataProvider, key string) (string, error) {
	if p.Spec.SecretRef == nil {
		return "", fmt.Errorf("%w: %s/%s (%s) has no secretRef", ErrProviderCredentials, p.Namespace, p.Name, p.Spec.Type)
	}
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: p.Spec.SecretRef.Name}, &s); err != nil {
		return "", fmt.Errorf("%w: get secret %s/%s: %w", ErrProviderCredentials, p.Namespace, p.Spec.SecretRef.Name, err)
	}
	v, ok := s.Data[key]
	if !ok {
		return "", fmt.Errorf("%w: secret %s/%s has no %q key", ErrProviderCredentials, p.Namespace, p.Spec.SecretRef.Name, key)
	}
	return string(v), nil
}
