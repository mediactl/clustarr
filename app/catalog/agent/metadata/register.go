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
// metadata gateway (rpc.catalogarr.metadata.* and catalogarr-metadata),
// catalogarr-markers, catalogarr-segments-result and catalogarr-artwork-fetch.
// Fixed at one replica (ADR-0007): its in-process rate limiters are what keep
// Clustarr inside every provider's quota, and artwork.Fetcher's in-process
// lock serialises the two artwork consumers per item. The orphan reaper is
// not here: it is the manager's (R9).
package metadata

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	catalogmetadata "github.com/mediactl/clustarr/app/catalog/metadata"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	"github.com/mediactl/clustarr/app/catalog/segmenting"
	markerworker "github.com/mediactl/clustarr/app/catalog/worker/markers"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// Options is what the metadata domain's Register takes.
type Options struct {
	k8s.Options
}

// Register adds the metadata gateway: every outbound metadata client, their
// rate limiters and the two cache tiers, serving rpc.catalogarr.metadata.*
// and the work.catalogarr.metadata.<tier> consumer; and the artwork store's
// gateway half (spec §B.3-§B.7) -- the Fetcher that both the metadata
// consumer and the catalogarr-artwork-fetch consumer store originals
// through, and that consumer itself.
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
// lock -- enough to serialise the two artwork consumers per item. The reaper
// is the manager's (R9, spec §5.13): one sweeper behind the lease, which the
// gateway's replica count never was.
func Register(_ context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
	if bus == nil {
		return catalogagent.Registration{}, errors.New("metadata domain: Register needs the bus")
	}
	fetcher := &artwork.Fetcher{
		Store:    bus.ObjectStore(events.BucketArtwork),
		HTTP:     artworkHTTPClient,
		Limiter:  artwork.NewHostLimiters(artworkHostRate, artworkHostBurst),
		Recorder: mgr.GetEventRecorder("metadata-gateway"),
	}
	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := catalogmetadata.Setup(ctx, catalogmetadata.Options{
			Client:     mgr.GetClient(),
			Reader:     mgr.GetAPIReader(),
			Bus:        bus,
			HTTPClient: metadataHTTPClient,
			Artwork:    fetcher,
			Markers: func(ctx context.Context, providers []pkgmetadata.MarkersProvider) (func(), error) {
				stopMarkers, err := markerworker.Setup(ctx, markerworker.Options{Bus: bus, Reader: mgr.GetAPIReader(), Client: mgr.GetClient()}, providers)
				if err != nil {
					return nil, err
				}
				// Segment analysis results write status.markers beside
				// TheIntroDB's handler, through the same merge. The results
				// half registers from the package it is in (loop spec §8.2
				// rule 1); F3.4 removes it with app/catalog/segmenting.
				stopResults, err := segmenting.Setup(ctx, segmenting.Options{
					Bus: bus, Reader: mgr.GetAPIReader(), Client: mgr.GetClient(),
				})
				if err != nil {
					stopMarkers()
					return nil, err
				}
				return func() { stopResults(); stopMarkers() }, nil
			},
		})
		if err != nil {
			return fmt.Errorf("catalogarr: metadata gateway: %w", err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	})); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("catalogarr: add the metadata gateway: %w", err)
	}

	// The ImportArtwork durable (spec §B.7): a reconciler saw status.artwork
	// drift from its sources (artwork.Drift). Same Fetcher, so the same per-item lock.
	spec, ok := o.BusTopology().Consumer(events.ConsumerCatalogArtworkFetch)
	if !ok {
		return catalogagent.Registration{}, fmt.Errorf("catalogarr: consumer %q missing from the bus topology", events.ConsumerCatalogArtworkFetch)
	}
	fetch := &artwork.Handler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Bus: bus, Fetcher: fetcher}
	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, spec.Subscription(), fetch.Handle)
		if err != nil {
			return fmt.Errorf("catalogarr: subscribe %s: %w", events.ConsumerCatalogArtworkFetch, err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	})); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("catalogarr: add the artwork fetch consumer: %w", err)
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
