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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// DefaultContactUserAgent is the User-Agent a seeded provider sends.
// MusicBrainz and Open Library refuse anonymous clients (the CRD's CEL
// requires contactUserAgent for both); the rest send it when set.
const DefaultContactUserAgent = "Clustarr ( https://github.com/mediactl/clustarr )"

// KeylessTypes are the provider types that work without credentials, each
// seeded by SeedDefaults: music (MusicBrainz, Cover Art Archive), books
// (Open Library), audiobooks (Audnexus), manga (MangaDex) and the anime id
// resolvers (AniList, Kitsu, Anime-Lists).
var KeylessTypes = []catalogv1alpha1.MetadataProviderType{
	catalogv1alpha1.MetadataProviderMusicBrainz,
	catalogv1alpha1.MetadataProviderCoverArt,
	catalogv1alpha1.MetadataProviderOpenLibrary,
	catalogv1alpha1.MetadataProviderAudnexus,
	catalogv1alpha1.MetadataProviderMangaDex,
	catalogv1alpha1.MetadataProviderAniList,
	catalogv1alpha1.MetadataProviderKitsu,
	catalogv1alpha1.MetadataProviderAnimeLists,
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=metadataproviders,verbs=create

// SeedDefaults creates, in namespace, a MetadataProvider named after each
// of KeylessTypes that namespace has no provider of, under any name. It
// only ever creates: a provider already there -- the owner's own, or a
// seed they edited or disabled -- is never changed. A seed the owner
// deletes comes back at the next start; spec.enabled false is how one is
// turned off. With namespace empty (`clustarr all` from a shell) it does
// nothing.
func SeedDefaults(ctx context.Context, c client.Client, namespace string) error {
	log := logging.FromContext(ctx)
	if namespace == "" {
		log.Info("metadataprovider: no namespace; keyless providers not seeded")
		return nil
	}
	var existing catalogv1alpha1.MetadataProviderList
	if err := c.List(ctx, &existing, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("metadataprovider: list providers: %w", err)
	}
	have := map[catalogv1alpha1.MetadataProviderType]bool{}
	for _, p := range existing.Items {
		have[p.Spec.Type] = true
	}
	for _, typ := range KeylessTypes {
		if have[typ] {
			continue
		}
		p := &catalogv1alpha1.MetadataProvider{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: string(typ)},
			Spec:       catalogv1alpha1.MetadataProviderSpec{Type: typ, ContactUserAgent: DefaultContactUserAgent},
		}
		if err := client.IgnoreAlreadyExists(c.Create(ctx, p)); err != nil {
			return fmt.Errorf("metadataprovider: seed %s: %w", typ, err)
		}
		log.Info("metadataprovider: seeded a keyless provider", "namespace", namespace, "type", typ)
	}
	return nil
}

// Bootstrap is a one-shot, leader-elected manager.Runnable that calls
// SeedDefaults, as qualityprofile.Bootstrap seeds the built-in profiles.
type Bootstrap struct {
	Client    client.Client
	Namespace string
}

func (b *Bootstrap) Start(ctx context.Context) error {
	return SeedDefaults(ctx, b.Client, b.Namespace)
}

func (b *Bootstrap) NeedLeaderElection() bool { return true }
