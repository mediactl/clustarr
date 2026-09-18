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
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/audnexus"
	"github.com/mediactl/clustarr/pkg/metadata/clients/comicvine"
	"github.com/mediactl/clustarr/pkg/metadata/clients/musicbrainz"
	"github.com/mediactl/clustarr/pkg/metadata/clients/openlibrary"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
)

// BuildRegistry lists every enabled MetadataProvider in namespace, sorted
// ascending by Priority within each kind (ties by Name for determinism), and
// constructs a metadata.Registry from them. It skips a disabled provider and
// a provider of a type Phase B did not ship a client for
// (ErrProviderNotImplemented) rather than failing the whole build -- one bad
// or unimplemented provider must not take every other one down.
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
			if err == ErrProviderNotImplemented {
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
		return ErrProviderNotImplemented
	}
	return nil
}
