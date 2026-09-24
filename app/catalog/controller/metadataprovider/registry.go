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
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
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
)

// BuildRegistry lists every enabled MetadataProvider in namespace, sorted
// ascending by Priority within each kind -- a tie going to a primary
// provider over a supplementary one (isSupplementary), then by Name for
// determinism -- and constructs a metadata.Registry from them. Every
// MetadataProviderType value has a client since task X6b; a disabled
// provider, or one of a type this package does not know
// (ErrProviderNotImplemented), is skipped rather than failing the whole
// build -- one bad or unknown provider must not take every other one down.
//
// It includes every enabled provider regardless of its last observed
// Ready/Authenticated/Throttled status: reachability is a per-call concern
// for metadata.Registry.Lookup's caller (a throttled provider fails its own
// calls; that is not a reason to leave it out of the registry), and this
// avoids the caller depending on this task's controller having reconciled
// recently.
//
// BuildRegistry is called by Task C5's metadata gateway, a SEPARATE process
// from the one this package's Reconciler runs in (§3/§12's
// `catalogarr-metadata` Deployment) -- it takes its own client.Client rather
// than reading any state this package's controller cached, because an
// in-process cache built here would not be reachable from that other
// process.
func BuildRegistry(ctx context.Context, c client.Client, namespace string, httpClient *http.Client) (*metadata.Registry, error) {
	var list catalogv1alpha1.MetadataProviderList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("metadataprovider: list: %w", err)
	}

	items := slices.Clone(list.Items)
	slices.SortFunc(items, func(a, b catalogv1alpha1.MetadataProvider) int {
		if a.Spec.Priority != b.Spec.Priority {
			return int(a.Spec.Priority) - int(b.Spec.Priority)
		}
		if sa, sb := isSupplementary(a.Spec.Type), isSupplementary(b.Spec.Type); sa != sb {
			if sb {
				return -1
			}
			return 1
		}
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})

	reg := &metadata.Registry{}
	for _, p := range items {
		if p.Spec.Enabled != nil && !*p.Spec.Enabled {
			continue
		}
		secretData, err := readSecret(ctx, c, p.Namespace, p.Spec.SecretRef)
		if err != nil {
			return nil, err
		}
		if err := addToRegistry(reg, p.Spec, secretData, httpClient); err != nil {
			if errors.Is(err, ErrProviderNotImplemented) {
				continue
			}
			return nil, fmt.Errorf("metadataprovider: build client for %s: %w", p.Name, err)
		}
	}
	return reg, nil
}

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

// addToRegistry mirrors NewProber's switch, but builds the raw client and
// appends it to the right Registry slice instead of wrapping it in Prober.
// The two switches share the same construction calls; kept separate rather
// than factored together because Prober's job (one cheap probe call) and
// Registry's job (the real, capability-typed client) return different things
// and unifying them would need an interface neither pkg/metadata nor this
// task's Prober actually needs.
func addToRegistry(reg *metadata.Registry, spec catalogv1alpha1.MetadataProviderSpec, secret map[string][]byte, httpClient *http.Client) error {
	limits := metadata.DefaultLimits()
	switch spec.Type {
	case catalogv1alpha1.MetadataProviderTMDB:
		c, err := tmdb.New(string(secret["apiKey"]), httpClient, baseURL(spec), limiterFor(spec, limits.TMDB, limits.TMDBBurst))
		if err != nil {
			return err
		}
		reg.Movies = append(reg.Movies, c)
	case catalogv1alpha1.MetadataProviderTVDB:
		reg.Series = append(reg.Series, tvdb.New(string(secret["apiKey"]), string(secret["pin"]), httpClient, baseURL(spec), limiterFor(spec, limits.TVDB, limits.TVDBBurst)))
	case catalogv1alpha1.MetadataProviderMusicBrainz:
		c, err := musicbrainz.New(spec.ContactUserAgent, httpClient, baseURL(spec), limiterFor(spec, limits.MusicBrainz, limits.MusicBrainzBurst))
		if err != nil {
			return err
		}
		reg.Artists = append(reg.Artists, c)
	case catalogv1alpha1.MetadataProviderOpenLibrary:
		reg.Books = append(reg.Books, openlibrary.New(spec.ContactUserAgent, httpClient, baseURL(spec), limiterFor(spec, limits.OpenLibrary, limits.OpenLibraryBurst)))
	case catalogv1alpha1.MetadataProviderComicVine:
		reg.Comics = append(reg.Comics, comicvine.New(string(secret["apiKey"]), httpClient, baseURL(spec), limiterFor(spec, limits.ComicVine, limits.ComicVineBurst)))
	case catalogv1alpha1.MetadataProviderAudnexus:
		reg.Audiobooks = append(reg.Audiobooks, audnexus.New(httpClient, baseURL(spec), limiterFor(spec, limits.Audnexus, limits.AudnexusBurst)))
	default:
		a, err := buildSupplementary(spec, secret, httpClient)
		if err != nil {
			return err
		}
		if a.artwork != nil {
			reg.Artwork = append(reg.Artwork, a.artwork)
		}
		if a.books != nil {
			reg.Books = append(reg.Books, a.books)
		}
		if a.comics != nil {
			reg.Comics = append(reg.Comics, a.comics)
		}
		if a.resolver != nil {
			reg.Resolvers = append(reg.Resolvers, a.resolver)
		}
	}
	return nil
}

// isSupplementary reports whether t is a provider no catalog CR is keyed
// by -- artwork-only, a secondary source for a kind keyed by another
// provider's id, or a pure crosswalk. It breaks priority ties: the
// Registry takes the first provider that answers, every MetadataProvider
// defaults to priority 50, and a default-priority Hardcover or Metron
// sorts by name ahead of Open Library or ComicVine and would answer a
// search with hits no CR can be created from. The gateway's own
// BuildRegistry (app/catalog/metadata/registry.go) applies the same rule.
func isSupplementary(t catalogv1alpha1.MetadataProviderType) bool {
	switch t {
	case catalogv1alpha1.MetadataProviderCoverArt, catalogv1alpha1.MetadataProviderFanart,
		catalogv1alpha1.MetadataProviderHardcover, catalogv1alpha1.MetadataProviderMetron,
		catalogv1alpha1.MetadataProviderAniList, catalogv1alpha1.MetadataProviderKitsu,
		catalogv1alpha1.MetadataProviderAnimeLists:
		return true
	default:
		return false
	}
}

// supplementary is one of the eight clients task X6b added, with each
// Registry slot it fills (nil where it fills none) and the cheapest call
// that proves it reachable.
type supplementary struct {
	artwork  metadata.ArtworkProvider
	books    metadata.BookProvider
	comics   metadata.ComicProvider
	resolver metadata.IDResolver
	ping     func(context.Context) error
}

// buildSupplementary builds the client for one of the eight provider types
// that shipped without one: coverart and fanart are Artwork; hardcover is
// Books; metron and mangadex are Comics and Resolvers; anilist, kitsu and
// animelists are Resolvers (AniList is a ComicProvider too, but a Comic's
// source can only be ComicVine or MangaDex, so it is not registered as a
// comic source). fanart needs secretRef key "apiKey"; hardcover and metron
// need "bearer". spec.contactUserAgent is sent as the User-Agent when set.
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
