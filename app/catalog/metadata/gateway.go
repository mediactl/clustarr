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
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/records"
)

// The gateway's own RBAC. It runs as RoleMetadata, in its own single-replica
// Deployment (§3), so it cannot rely on the MetadataProvider controller's
// markers being in force in the same pod -- even though controller-gen folds
// every marker into one ClusterRole today.
//
// The eight kinds with metadata of their own are read, never written: the
// gateway answers in clustarr-item-metadata, and the item keys (the Artist
// and Author reconcilers for theirs) apply status.metadata under
// catalog-metadata in the manager (ADR-0019 §7.1, A5.1).
//
// Provider Secrets are read by name (registry.go), uncached in an agent
// (k8s.AgentManagerOptions), so get is the only verb: an agent role's
// secrets grant is get alone (ADR-0019 §9.1, A7.2).
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=metadataproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;artists;albums;authors;books;audiobooks;comics,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// Options configures Setup. Client, Reader and Bus are required;
// everything else defaults.
type Options struct {
	Client client.Client
	// Reader is the uncached reader a Series refresh lists its episodes
	// through (mgr.GetAPIReader()).
	Reader     client.Reader
	Bus        events.Bus
	HTTPClient *http.Client
	L1Size     int
	Clock      clockwork.Clock
	// Markers starts the marker worker (plex-analyze-bypass §3.4) over the
	// registry's Markers providers: app/catalog/run.go binds
	// app/catalog/worker/markers.Setup. Nil starts none.
	Markers func(ctx context.Context, providers []pkgmetadata.MarkersProvider) (stop func(), err error)
	// ImageSets starts the image-set worker (spec 2026-10-22) over the
	// registry's ImageSets providers: app/catalog/agent/metadata binds
	// app/catalog/worker/imagesets.Setup. Nil starts none.
	ImageSets func(ctx context.Context, providers []pkgmetadata.ImageSetProvider) (stop func(), err error)
	// Records writes the item-metadata records (ADR-0019 §7.1); required.
	// The register shares it with the artwork fetcher, one process.
	Records *records.Writer[*schema.ItemMetadataRecord]
}

// Setup builds the metadata gateway (the Registry from every enabled
// MetadataProvider, the two-tier cache, the work-queue Handler and the RPC
// responders) and starts consuming. It is RoleMetadata's entire job;
// app/catalog/run.go's setupWorkers is expected to call this once, guarded
// by o.Role.Has(catalog.RoleMetadata) (a different Phase C task's path --
// see "Interfaces -- Produces").
func Setup(ctx context.Context, o Options) (stop func(), err error) {
	if o.Client == nil || o.Bus == nil {
		return nil, fmt.Errorf("metadata: Client and Bus are required")
	}
	if o.Reader == nil {
		return nil, fmt.Errorf("metadata: Reader is required -- pass the uncached API reader (mgr.GetAPIReader())")
	}
	if o.Records == nil {
		return nil, fmt.Errorf("metadata: Records is required -- the gateway answers in %s", events.BucketItemMetadata)
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
	reg, err := BuildRegistryWith(ctx, o.Client, providers.Items, httpClient, RegistryDeps{MediuxKV: o.Bus.KV(events.BucketMediux)})
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
		Records:  o.Records,
		Extended: o.Bus.KV(events.BucketMetadataExtended),
	}
	stopSub, err := o.Bus.Subscribe(ctx, spec.Subscription(), h.Handle)
	if err != nil {
		return nil, fmt.Errorf("metadata: subscribe: %w", err)
	}

	stopMarkers := func() {}
	if o.Markers != nil {
		if stopMarkers, err = o.Markers(ctx, reg.Markers); err != nil {
			stopSub()
			return nil, fmt.Errorf("metadata: %w", err)
		}
	}

	stopImageSets := func() {}
	if o.ImageSets != nil {
		if stopImageSets, err = o.ImageSets(ctx, reg.ImageSets); err != nil {
			stopSub()
			stopMarkers()
			return nil, fmt.Errorf("metadata: %w", err)
		}
	}

	stopRPC, err := ServeRPC(o.Bus, reg)
	if err != nil {
		stopSub()
		stopMarkers()
		stopImageSets()
		return nil, err
	}
	stopExtras, err := ServeExtras(o.Bus, o.Bus.KV(events.BucketPlexExtras), reg, clock)
	if err != nil {
		stopRPC()
		stopSub()
		stopMarkers()
		stopImageSets()
		return nil, err
	}
	// The responders drain with the consumers (ADR-0020 §1): a request
	// already buffered when the agent stops is still answered.
	return func() { stopConcurrently(stopRPC, stopExtras, stopSub, stopMarkers, stopImageSets) }, nil
}
