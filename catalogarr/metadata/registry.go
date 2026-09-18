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
	"fmt"
	"net/http"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/audnexus"
	"github.com/mediactl/clustarr/pkg/metadata/clients/comicvine"
	"github.com/mediactl/clustarr/pkg/metadata/clients/musicbrainz"
	"github.com/mediactl/clustarr/pkg/metadata/clients/openlibrary"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
)

// BuildRegistry constructs a pkg/metadata.Registry from the cluster's
// MetadataProvider objects, in ascending spec.priority order (lower wins,
// matching Registry.Lookup's "first in the slice, first tried"). Only the
// six types with a Phase B client are wired (see Judgment call 5 in this
// task's plan); an enabled provider of any other type is skipped, not an
// error, so an operator can create the CR ahead of the client landing.
func BuildRegistry(ctx context.Context, c client.Client, providers []catalogv1alpha1.MetadataProvider, httpClient *http.Client) (*pkgmetadata.Registry, error) {
	sorted := make([]catalogv1alpha1.MetadataProvider, len(providers))
	copy(sorted, providers)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Spec.Priority < sorted[j].Spec.Priority })

	reg := &pkgmetadata.Registry{}
	for _, p := range sorted {
		if p.Spec.Enabled != nil && !*p.Spec.Enabled {
			continue
		}
		limiter := resolveLimiter(p.Spec.Type, p.Spec.RateLimit)
		switch p.Spec.Type {
		case catalogv1alpha1.MetadataProviderTMDB:
			key, err := secretValue(ctx, c, p, catalogv1alpha1.MetadataSecretKeyAPIKey)
			if err != nil {
				return nil, err
			}
			cl, err := tmdb.New(key, httpClient, baseURL(p, "https://api.themoviedb.org/3"), limiter)
			if err != nil {
				return nil, fmt.Errorf("metadata: build tmdb client for %s/%s: %w", p.Namespace, p.Name, err)
			}
			reg.Movies = append(reg.Movies, cl)
		case catalogv1alpha1.MetadataProviderTVDB:
			key, err := secretValue(ctx, c, p, catalogv1alpha1.MetadataSecretKeyAPIKey)
			if err != nil {
				return nil, err
			}
			pin, _ := secretValue(ctx, c, p, catalogv1alpha1.MetadataSecretKeyPin)
			reg.Series = append(reg.Series, tvdb.New(key, pin, httpClient, baseURL(p, "https://api4.thetvdb.com/v4"), limiter))
		case catalogv1alpha1.MetadataProviderMusicBrainz:
			cl, err := musicbrainz.New(p.Spec.ContactUserAgent, httpClient, baseURL(p, "https://musicbrainz.org/ws/2"), limiter)
			if err != nil {
				return nil, fmt.Errorf("metadata: build musicbrainz client for %s/%s: %w", p.Namespace, p.Name, err)
			}
			reg.Artists = append(reg.Artists, cl)
		case catalogv1alpha1.MetadataProviderOpenLibrary:
			reg.Books = append(reg.Books, openlibrary.New(p.Spec.ContactUserAgent, httpClient, baseURL(p, "https://openlibrary.org"), limiter))
		case catalogv1alpha1.MetadataProviderAudnexus:
			reg.Audiobooks = append(reg.Audiobooks, audnexus.New(httpClient, baseURL(p, "https://api.audnex.us"), limiter))
		case catalogv1alpha1.MetadataProviderComicVine:
			key, err := secretValue(ctx, c, p, catalogv1alpha1.MetadataSecretKeyAPIKey)
			if err != nil {
				return nil, err
			}
			reg.Comics = append(reg.Comics, comicvine.New(key, httpClient, baseURL(p, "https://comicvine.gamespot.com/api"), limiter))
		default:
			continue
		}
	}
	return reg, nil
}

func baseURL(p catalogv1alpha1.MetadataProvider, def string) string {
	if p.Spec.BaseURL != nil && *p.Spec.BaseURL != "" {
		return *p.Spec.BaseURL
	}
	return def
}

func secretValue(ctx context.Context, c client.Client, p catalogv1alpha1.MetadataProvider, key string) (string, error) {
	if p.Spec.SecretRef == nil {
		return "", fmt.Errorf("metadata: %s/%s (%s) has no secretRef", p.Namespace, p.Name, p.Spec.Type)
	}
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: p.Spec.SecretRef.Name}, &s); err != nil {
		return "", fmt.Errorf("metadata: get secret %s/%s: %w", p.Namespace, p.Spec.SecretRef.Name, err)
	}
	v, ok := s.Data[key]
	if !ok {
		return "", fmt.Errorf("metadata: secret %s/%s has no %q key", p.Namespace, p.Spec.SecretRef.Name, key)
	}
	return string(v), nil
}
