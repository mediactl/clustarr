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
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/anilist"
	"github.com/mediactl/clustarr/pkg/metadata/clients/animelists"
	"github.com/mediactl/clustarr/pkg/metadata/clients/coverart"
	"github.com/mediactl/clustarr/pkg/metadata/clients/fanart"
	"github.com/mediactl/clustarr/pkg/metadata/clients/hardcover"
	"github.com/mediactl/clustarr/pkg/metadata/clients/kitsu"
	"github.com/mediactl/clustarr/pkg/metadata/clients/mangadex"
	"github.com/mediactl/clustarr/pkg/metadata/clients/mdblist"
	"github.com/mediactl/clustarr/pkg/metadata/clients/metron"
	plexclient "github.com/mediactl/clustarr/pkg/metadata/clients/plex"
	"github.com/mediactl/clustarr/pkg/metadata/clients/theintrodb"
)

func readSecret(ctx context.Context, c client.Client, ns string, ref *corev1.LocalObjectReference) (map[string][]byte, error) {
	if ref == nil {
		return nil, nil
	}
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("metadataprovider: secret %s/%s not found", ns, ref.Name)
		}
		return nil, err
	}
	return s.Data, nil
}

// supplementary is one of the eight clients task X6b added, or mdblist's
// ratings client (omdb's waits on its recorded shapes), with each
// Registry slot it fills (nil where it fills none) and the cheapest call
// that proves it reachable.
type supplementary struct {
	artwork  metadata.ArtworkProvider
	books    metadata.BookProvider
	comics   metadata.ComicProvider
	resolver metadata.IDResolver
	ratings  metadata.RatingsProvider
	markers  metadata.MarkersProvider
	plex     metadata.PlexProvider
	ping     func(context.Context) error
}

// buildSupplementary builds the client for one of the eight provider types
// that shipped without one: coverart and fanart are Artwork; hardcover is
// Books; metron and mangadex are Comics and Resolvers; anilist, kitsu and
// animelists are Resolvers (AniList is a ComicProvider too, but a Comic's
// source can only be ComicVine or MangaDex, so it is not registered as a
// comic source); mdblist is Ratings; plex is Resolvers and Plex. fanart and mdblist need secretRef key
// "apiKey" (mdblist also reads an optional "apiKeySecondary"); hardcover and
// metron need "bearer". spec.contactUserAgent is sent as the User-Agent when set.
// With no spec.rateLimit each client gets its own package's DefaultRate and
// DefaultBurst. Any other type is ErrProviderNotImplemented.
func buildSupplementary(spec catalogv1alpha1.MetadataProviderSpec, secret map[string][]byte, httpClient *http.Client) (*supplementary, error) {
	ua := spec.ContactUserAgent
	switch spec.Type {
	case catalogv1alpha1.MetadataProviderCoverArt:
		c := coverart.New(coverart.Config{HTTPClient: httpClient, BaseURL: baseURL(spec), Limiter: limiterFor(spec, coverart.DefaultRate, coverart.DefaultBurst), UserAgent: ua})
		return &supplementary{artwork: c, ping: c.Ping}, nil
	case catalogv1alpha1.MetadataProviderFanart:
		c, err := fanart.New(fanart.Config{HTTPClient: httpClient, BaseURL: baseURL(spec), Limiter: limiterFor(spec, fanart.DefaultRate, fanart.DefaultBurst), UserAgent: ua, APIKey: string(secret[catalogv1alpha1.MetadataSecretKeyAPIKey])})
		if err != nil {
			return nil, fmt.Errorf("fanart requires secretRef key %s: %w", catalogv1alpha1.MetadataSecretKeyAPIKey, err)
		}
		return &supplementary{artwork: c, ping: c.Ping}, nil
	case catalogv1alpha1.MetadataProviderHardcover:
		c, err := hardcover.New(hardcover.Config{HTTPClient: httpClient, BaseURL: baseURL(spec), Limiter: limiterFor(spec, hardcover.DefaultRate, hardcover.DefaultBurst), UserAgent: ua, Token: string(secret[catalogv1alpha1.MetadataSecretKeyBearer])})
		if err != nil {
			return nil, fmt.Errorf("hardcover requires secretRef key %s: %w", catalogv1alpha1.MetadataSecretKeyBearer, err)
		}
		return &supplementary{books: c, ping: c.Ping}, nil
	case catalogv1alpha1.MetadataProviderMetron:
		c, err := metron.New(metron.Config{HTTPClient: httpClient, BaseURL: baseURL(spec), Limiter: limiterFor(spec, metron.DefaultRate, metron.DefaultBurst), UserAgent: ua, Token: string(secret[catalogv1alpha1.MetadataSecretKeyBearer])})
		if err != nil {
			return nil, fmt.Errorf("metron requires secretRef key %s: %w", catalogv1alpha1.MetadataSecretKeyBearer, err)
		}
		return &supplementary{comics: c, resolver: c, ping: c.Ping}, nil
	case catalogv1alpha1.MetadataProviderMangaDex:
		c := mangadex.New(mangadex.Config{HTTPClient: httpClient, BaseURL: baseURL(spec), Limiter: limiterFor(spec, mangadex.DefaultRate, mangadex.DefaultBurst), UserAgent: ua})
		return &supplementary{comics: c, resolver: c, ping: c.Ping}, nil
	case catalogv1alpha1.MetadataProviderAniList:
		c := anilist.New(anilist.Config{HTTPClient: httpClient, BaseURL: baseURL(spec), Limiter: limiterFor(spec, anilist.DefaultRate, anilist.DefaultBurst), UserAgent: ua})
		return &supplementary{resolver: c, ping: c.Ping}, nil
	case catalogv1alpha1.MetadataProviderKitsu:
		c := kitsu.New(kitsu.Config{HTTPClient: httpClient, BaseURL: baseURL(spec), Limiter: limiterFor(spec, kitsu.DefaultRate, kitsu.DefaultBurst), UserAgent: ua})
		return &supplementary{resolver: c, ping: c.Ping}, nil
	case catalogv1alpha1.MetadataProviderAnimeLists:
		c := animelists.New(animelists.Config{HTTPClient: httpClient, URL: baseURL(spec), Limiter: limiterFor(spec, animelists.DefaultRate, animelists.DefaultBurst), UserAgent: ua})
		return &supplementary{resolver: c, ping: c.Ping}, nil
	case catalogv1alpha1.MetadataProviderMDBList:
		c, err := mdblist.New(mdblist.Config{
			HTTPClient: httpClient, BaseURL: baseURL(spec), Limiter: limiterFor(spec, mdblist.DefaultRate, mdblist.DefaultBurst), UserAgent: ua,
			APIKeys: []string{string(secret[catalogv1alpha1.MetadataSecretKeyAPIKey]), string(secret[catalogv1alpha1.MetadataSecretKeyAPIKeySecondary])},
		})
		if err != nil {
			return nil, fmt.Errorf("mdblist requires secretRef key %s: %w", catalogv1alpha1.MetadataSecretKeyAPIKey, err)
		}
		return &supplementary{ratings: c, ping: c.Ping}, nil
	case catalogv1alpha1.MetadataProviderTheIntroDB:
		c, err := theintrodb.New(theintrodb.Config{
			HTTPClient: httpClient, BaseURL: baseURL(spec), Limiter: limiterFor(spec, theintrodb.DefaultRate, theintrodb.DefaultBurst), UserAgent: ua,
			APIKeys: theintrodb.Keys(string(secret[catalogv1alpha1.MetadataSecretKeyAPIKey]), string(secret[catalogv1alpha1.MetadataSecretKeyAPIKeys])),
		})
		if err != nil {
			return nil, fmt.Errorf("theintrodb: %w", err)
		}
		return &supplementary{markers: c, ping: c.Ping}, nil
	case catalogv1alpha1.MetadataProviderPlex:
		c, err := plexclient.New(plexclient.Config{
			HTTPClient: httpClient, BaseURL: baseURL(spec), Limiter: limiterFor(spec, plexclient.DefaultRate, plexclient.DefaultBurst), UserAgent: ua,
			Token: string(secret[catalogv1alpha1.MetadataSecretKeyToken]),
		})
		if err != nil {
			return nil, fmt.Errorf("plex requires secretRef key %s: %w", catalogv1alpha1.MetadataSecretKeyToken, err)
		}
		return &supplementary{resolver: c, plex: c, ping: c.Ping}, nil
	case catalogv1alpha1.MetadataProviderOMDb:
		// Ruling R5 (spec §C.3): the CRD enum member and secretRef shape
		// exist, but no client is written against no recorded response
		// shape. See ErrProviderAwaitingFixtures' doc comment (prober.go).
		return nil, fmt.Errorf("metadataprovider: %s: %w", spec.Type, ErrProviderAwaitingFixtures)
	default:
		return nil, ErrProviderNotImplemented
	}
}

// pingProber is the Prober for a supplementary provider: its client's own
// Ping, the cheapest call that proves it reachable and, where it takes
// credentials, that they are accepted.
type pingProber struct{ ping func(context.Context) error }

func (p pingProber) Probe(ctx context.Context) (ProbeResult, error) {
	return ProbeResult{}, p.ping(ctx)
}

// newSupplementaryProber builds the Prober for one of the eight provider
// types buildSupplementary covers, or ErrProviderNotImplemented for any
// other. It is NewProber's default case (prober.go).
func newSupplementaryProber(spec catalogv1alpha1.MetadataProviderSpec, secret map[string][]byte, httpClient *http.Client) (Prober, error) {
	a, err := buildSupplementary(spec, secret, httpClient)
	if err != nil {
		return nil, err
	}
	return pingProber{a.ping}, nil
}
