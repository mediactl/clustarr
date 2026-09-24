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

	"github.com/jonboulle/clockwork"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	"github.com/mediactl/clustarr/pkg/events"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// The gateway's own RBAC. It runs as RoleMetadata, in its own single-replica
// Deployment (§3), so it cannot rely on the MetadataProvider controller's
// markers being in force in the same pod -- even though controller-gen folds
// every marker into one ClusterRole today.
//
// The six non-video kinds (artists, albums, authors, books, audiobooks,
// comics) share the movies/series lines below rather than getting their own:
// newTarget (target.go) and Handle's per-kind switch (worker.go) read and
// PatchStatus all eight kinds identically, through the one
// k8s.ManagerCatalogarrMetadata field manager, so their RBAC needs are the
// same get;list;watch / status get;update;patch shape movies and series
// already had.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=metadataproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;artists;albums;authors;books;audiobooks;comics,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies/status;series/status;artists/status;albums/status;authors/status;books/status;audiobooks/status;comics/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Options configures Setup. Client and Bus are required; everything else
// defaults.
type Options struct {
	Client client.Client
	// Reader is the uncached reader the artwork pass re-reads an item
	// through before its apply (mgr.GetAPIReader()). Nil uses Client.
	Reader     client.Reader
	Bus        events.Bus
	HTTPClient *http.Client
	L1Size     int
	Clock      clockwork.Clock
	// Artwork fetches artwork originals after each metadata fetch (spec
	// §B.4). Nil fetches none -- status.artwork is still re-declared on
	// every apply, never omitted.
	Artwork *artwork.Fetcher
}

// Setup builds the metadata gateway (the Registry from every enabled
// MetadataProvider, the two-tier cache, the work-queue Handler and the RPC
// responders) and starts consuming. It is RoleMetadata's entire job;
// app/catalog/run.go's setupWorkers is expected to call this once, guarded
// by o.Role.Has(catalogarr.RoleMetadata) (a different Phase C task's path --
// see "Interfaces -- Produces").
func Setup(ctx context.Context, o Options) (stop func(), err error) {
	if o.Client == nil || o.Bus == nil {
		return nil, fmt.Errorf("metadata: Client and Bus are required")
	}
	httpClient := o.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	clock := o.Clock
	if clock == nil {
		clock = clockwork.NewRealClock()
	}
	l1Size := o.L1Size
	if l1Size <= 0 {
		l1Size = 4096 // a homelab-sized working set; tune via Options.L1Size
	}

	var providers catalogv1alpha1.MetadataProviderList
	if err := o.Client.List(ctx, &providers); err != nil {
		return nil, fmt.Errorf("metadata: list MetadataProviders: %w", err)
	}
	reg, err := BuildRegistry(ctx, o.Client, providers.Items, httpClient)
	if err != nil {
		return nil, err
	}

	l1, err := pkgmetadata.NewLRUCache(l1Size, clock)
	if err != nil {
		return nil, fmt.Errorf("metadata: build L1 cache: %w", err)
	}
	cache := newTieredCache(l1, newKVCache(o.Bus.KV(events.BucketMetadataCache), clock))

	spec, ok := events.Default().Consumer(events.ConsumerCatalogMetadata)
	if !ok {
		return nil, fmt.Errorf("metadata: consumer %q missing from the default topology", events.ConsumerCatalogMetadata)
	}
	h := &Handler{
		Client: o.Client, Reader: o.Reader, Registry: reg, Cache: cache,
		Artwork: o.Artwork, Bus: o.Bus,
	}
	stopSub, err := o.Bus.Subscribe(ctx, spec.Subscription(), h.Handle)
	if err != nil {
		return nil, fmt.Errorf("metadata: subscribe: %w", err)
	}

	if err := ServeRPC(o.Bus, reg); err != nil {
		stopSub()
		return nil, err
	}
	return stopSub, nil
}
