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
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/plexextras"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// extrasFreshness is how long a stored answer is served before Plex is
// asked again. Plex's extras change rarely, and a library refresh asks for
// every item, so a week keeps plex.tv to one request per item per week.
const extrasFreshness = 7 * 24 * time.Hour

// extrasService answers rpc.catalogarr.metadata.extras: an item's extras
// from the clustarr-plex-extras bucket, fetched from Plex's metadata
// service and stored when the bucket has none or only a stale entry.
type extrasService struct {
	kv        events.KV
	providers []pkgmetadata.PlexExtrasProvider
	clock     clockwork.Clock
}

// ServeExtras registers rpc.catalogarr.metadata.extras in the catalogarr
// queue group over the bucket kv and the registry's Plex providers. PMS
// asks the ui's Plex provider for an item's extras on every refresh and
// deletes the ones it holds when the answer is empty, so an error is
// answered as an error -- a stale entry when there is one -- and never as
// an empty list.
func ServeExtras(bus events.Requester, kv events.KV, reg *pkgmetadata.Registry, clock clockwork.Clock) error {
	s := &extrasService{kv: kv, clock: clock}
	for _, p := range reg.Plex {
		if e, ok := p.(pkgmetadata.PlexExtrasProvider); ok {
			s.providers = append(s.providers, e)
		}
	}
	err := bus.Serve(events.RPCMetadataExtras, events.QueueGroupCatalog, func(ctx context.Context, data []byte) ([]byte, error) {
		ctx, span := tracing.Start(ctx, "metadata.rpc.serve.extras")
		defer span.End()
		var req schema.PlexExtrasRequest
		if err := json.Unmarshal(data, &req); err != nil {
			tracing.RecordError(span, err)
			return nil, fmt.Errorf("metadata: decode PlexExtrasRequest: %w", err)
		}
		resp := s.get(ctx, req.PlexID)
		if resp.Error != "" {
			tracing.RecordError(span, errors.New(resp.Error))
		}
		return json.Marshal(resp)
	})
	if err != nil {
		return fmt.Errorf("metadata: serve %s: %w", events.RPCMetadataExtras, err)
	}
	return nil
}

func (s *extrasService) get(ctx context.Context, plexID string) schema.PlexExtrasResponse {
	if plexID == "" {
		return schema.PlexExtrasResponse{Error: "metadata: extras need a Plex id"}
	}
	log := logging.FromContext(ctx)
	key := plexextras.Key(plexID)

	stored, have := s.read(ctx, key)
	if have && s.clock.Since(stored.FetchedAt) < extrasFreshness {
		return schema.PlexExtrasResponse{Extras: stored.Extras, FetchedAt: stored.FetchedAt}
	}

	extras, err := s.fetch(ctx, plexID)
	if err != nil {
		if have {
			log.WarnContext(ctx, "metadata: refetch Plex extras; serving the stored answer", "plexID", plexID, "fetchedAt", stored.FetchedAt, "error", err)
			return schema.PlexExtrasResponse{Extras: stored.Extras, FetchedAt: stored.FetchedAt}
		}
		return schema.PlexExtrasResponse{Error: err.Error()}
	}

	entry := plexextras.Entry{FetchedAt: s.clock.Now(), Extras: extras}
	if b, err := plexextras.Encode(entry); err != nil {
		log.WarnContext(ctx, "metadata: encode Plex extras (not stored)", "plexID", plexID, "error", err)
	} else if _, err := s.kv.Put(ctx, key, b); err != nil {
		// Not stored means asked again next time, which is the cost; the
		// answer itself is good.
		log.WarnContext(ctx, "metadata: store Plex extras", "plexID", plexID, "error", err)
	}
	if entry.Extras == nil {
		entry.Extras = []json.RawMessage{}
	}
	return schema.PlexExtrasResponse{Extras: entry.Extras, FetchedAt: entry.FetchedAt}
}

// read returns the stored entry; an unreadable one counts as none, so it is
// fetched again and replaced.
func (s *extrasService) read(ctx context.Context, key string) (plexextras.Entry, bool) {
	e, err := s.kv.Get(ctx, key)
	if err != nil {
		if !errors.Is(err, events.ErrKeyNotFound) {
			logging.FromContext(ctx).WarnContext(ctx, "metadata: read stored Plex extras", "key", key, "error", err)
		}
		return plexextras.Entry{}, false
	}
	entry, err := plexextras.Decode(e.Value)
	if err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "metadata: decode stored Plex extras", "key", key, "error", err)
		return plexextras.Entry{}, false
	}
	return entry, true
}

// fetch asks each Plex provider in turn, the first answer winning.
func (s *extrasService) fetch(ctx context.Context, plexID string) ([]json.RawMessage, error) {
	if len(s.providers) == 0 {
		return nil, errors.New("metadata: no plex metadata provider is configured to fetch extras with")
	}
	var errs []error
	for _, p := range s.providers {
		pCtx, span := tracing.Start(ctx, "metadata.PlexExtrasProvider.Extras")
		extras, err := p.Extras(pCtx, plexID)
		if err != nil {
			tracing.RecordError(span, err)
			span.End()
			errs = append(errs, err)
			continue
		}
		span.End()
		return extras, nil
	}
	return nil, fmt.Errorf("metadata: fetch Plex extras for %s: %w", plexID, errors.Join(errs...))
}
