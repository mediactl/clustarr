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

// Package metadata is the agent's metadata domain (spec §3.5.3): the
// metadata gateway (rpc.catalog.metadata.* and catalog-metadata, answering
// in clustarr-item-metadata), catalog-markers (answering into
// clustarr-markers) and catalog-artwork-fetch (storing originals). It
// writes no Kubernetes object (ADR-0019).
// Fixed at one replica (ADR-0007): its in-process rate limiters are what keep
// Clustarr inside every provider's quota, and artwork.Fetcher's in-process
// lock serialises two fetch tasks of one item. The orphan reaper is not
// here: it is the manager's (R9).
package metadata

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	catalogmetadata "github.com/mediactl/clustarr/app/catalog/metadata"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	imagesetworker "github.com/mediactl/clustarr/app/catalog/worker/imagesets"
	markerworker "github.com/mediactl/clustarr/app/catalog/worker/markers"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/version"
)

// Options is what the metadata domain's Register takes.
type Options struct {
	k8s.Options
}

// Register adds the metadata gateway: every outbound metadata client, their
// rate limiters and the two cache tiers, serving rpc.catalog.metadata.*
// and the work.catalog.metadata.<tier> consumer; and the artwork store's
// gateway half (spec §B.3-§B.7) -- the catalog-artwork-fetch consumer and
// the Fetcher it stores the originals the item key names through. Both
// answer in clustarr-item-metadata through one records writer.
//
// Both are k8s.EveryReplica: the gateway lists MetadataProviders through the
// cache, so it starts only after the caches sync, and it is pinned to one
// replica by the Deployment, not by a lease.
//
// The gateway is a Runnable rather than a direct call because
// catalogmetadata.Setup Lists MetadataProviders through the manager's
// client: called before mgr.Start it would read an unsynced cache and build
// a registry with no providers in it. Runnables added this way start only
// after the caches have synced, and the subscription's lifetime is then the
// manager's.
//
// §3 pins this domain to exactly one replica -- its in-process rate limiters
// are what keep Clustarr inside every provider's quota -- which is why
// `--role all` is not what the manifests run for the main Deployment. It is
// pinned by the Deployment's replica count, NOT by the leader lease, so the
// runnable is a k8s.EveryReplica: a plain manager.RunnableFunc would go behind
// the lease (see EveryReplica). The metadata Deployment runs --role metadata,
// which does not elect, and controller-runtime treats a non-electing process
// as elected -- so the gateway did start there. The exposure is a role that
// elects and also serves metadata: one replica would serve, the rest idle.
//
// The one replica is also what makes artwork.Fetcher.Lock -- an in-process
// lock -- enough to serialise two fetch tasks of one item. The reaper
// is the manager's (R9, spec §5.13): one sweeper behind the lease, which the
// gateway's replica count never was.
func Register(_ context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
	if bus == nil {
		return catalogagent.Registration{}, errors.New("metadata domain: Register needs the bus")
	}
	// The gateway's one writer of clustarr-item-metadata (ADR-0019 §7.1):
	// the metadata handler answers through it, and the artwork fetcher
	// records its failures through it, one process, one compare-and-swap.
	answers := records.NewWriter(bus.KV(events.BucketItemMetadata), agentrecords.ItemMetadata(),
		os.Getenv("POD_NAME"), version.String())
	fetcher := &artwork.Fetcher{
		Store:   bus.ObjectStore(events.BucketArtwork),
		HTTP:    artworkHTTPClient,
		Limiter: artwork.NewHostLimiters(artworkHostRate, artworkHostBurst),
	}
	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		start := func(ctx context.Context) (func(), error) {
			stop, err := catalogmetadata.Setup(ctx, catalogmetadata.Options{
				Client:     mgr.GetClient(),
				Reader:     mgr.GetAPIReader(),
				Bus:        bus,
				HTTPClient: metadataHTTPClient,
				Records:    answers,
				// TheIntroDB's handler answers into clustarr-markers; the
				// remediation loop incorporates (loop spec §4.12).
				Markers: func(ctx context.Context, providers []pkgmetadata.MarkersProvider) (func(), error) {
					return markerworker.Setup(ctx, markerworker.Options{Bus: bus, Writer: os.Getenv("POD_NAME")}, providers)
				},
				// MediUX's handler answers into clustarr-image-sets; the
				// remediation loop's image-set stage incorporates.
				ImageSets: func(ctx context.Context, providers []pkgmetadata.ImageSetProvider) (func(), error) {
					return imagesetworker.Setup(ctx, imagesetworker.Options{Bus: bus, Writer: os.Getenv("POD_NAME")}, providers)
				},
			})
			if err != nil {
				return nil, fmt.Errorf("catalog: metadata gateway: %w", err)
			}
			return stop, nil
		}
		fingerprint := func(ctx context.Context) (string, error) {
			var providers catalogv1alpha1.MetadataProviderList
			if err := mgr.GetClient().List(ctx, &providers); err != nil {
				return "", err
			}
			return providerFingerprint(providers.Items), nil
		}
		return superviseGateway(ctx, gatewayRebuildEvery, fingerprint, start)
	})); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("catalog: add the metadata gateway: %w", err)
	}

	// The artwork-fetch durable (ADR-0019 §7.1): the item key names the
	// originals to fetch; the fetcher stores them and records its failures
	// through the same item-metadata writer.
	spec, ok := o.BusTopology().Consumer(events.ConsumerCatalogArtworkFetch)
	if !ok {
		return catalogagent.Registration{}, fmt.Errorf("catalog: consumer %q missing from the bus topology", events.ConsumerCatalogArtworkFetch)
	}
	fetch := &artwork.Handler{Fetcher: fetcher, Records: answers}
	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, spec.Subscription(), fetch.Handle)
		if err != nil {
			return fmt.Errorf("catalog: subscribe %s: %w", events.ConsumerCatalogArtworkFetch, err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	})); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("catalog: add the artwork fetch consumer: %w", err)
	}
	return catalogagent.Registration{}, nil
}

// metadataHTTPClient is the outbound client the metadata gateway's provider
// clients call with. It is a named value rather than http.DefaultClient so a
// misbehaving provider cannot hang a handler indefinitely.
var metadataHTTPClient = &http.Client{Timeout: 30 * time.Second}

// artworkHTTPClient fetches artwork originals. It is not metadataHTTPClient:
// an image of up to artwork.MaxImageBytes from a CDN is a longer transfer
// than a metadata API call, and a stuck one must not hold a gateway handler
// (and the item's artwork lock) past this timeout.
var artworkHTTPClient = &http.Client{Timeout: 60 * time.Second}

// Each image host gets its own token bucket. Image CDNs are not the metadata
// APIs whose MetadataProvider limits the registry applies, and a custom
// spec.artwork URL may name any host; four a second with a burst of four is
// polite to a CDN and still fetches an item's nine types in about two
// seconds.
const (
	artworkHostRate  = 4
	artworkHostBurst = 4
)
