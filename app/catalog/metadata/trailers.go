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

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/trailers"
)

// trailersService answers rpc.catalog.metadata.trailers: the videos the
// registry's trailer providers name for a Movie or Series.
type trailersService struct {
	providers []pkgmetadata.TrailerProvider
}

// ServeTrailers registers rpc.catalog.metadata.trailers in the catalog
// queue group over the registry's trailer providers (TMDB, TheTVDB). The
// trailers agent asks it for an item Plex's metadata service has no
// trailer for, and records "none" on an empty answer, so a provider that
// could not be asked is answered as an error unless another named a video.
// stop drains the responder (ADR-0020 §1).
func ServeTrailers(bus events.Requester, reg *pkgmetadata.Registry) (stop func(), err error) {
	s := &trailersService{providers: reg.Trailers}
	stop, err = bus.Serve(events.RPCMetadataTrailers, events.QueueGroupCatalog, func(ctx context.Context, data []byte) ([]byte, error) {
		ctx, span := tracing.Start(ctx, "metadata.rpc.serve.trailers")
		defer span.End()
		var req schema.TrailersRequest
		if err := json.Unmarshal(data, &req); err != nil {
			tracing.RecordError(span, err)
			return nil, fmt.Errorf("metadata: decode TrailersRequest: %w", err)
		}
		resp := s.get(ctx, req)
		if resp.Error != "" {
			tracing.RecordError(span, errors.New(resp.Error))
		}
		return json.Marshal(resp)
	})
	if err != nil {
		return nil, fmt.Errorf("metadata: serve %s: %w", events.RPCMetadataTrailers, err)
	}
	return stop, nil
}

func (s *trailersService) get(ctx context.Context, req schema.TrailersRequest) schema.TrailersResponse {
	if req.Kind != commonv1.MediaKindMovie && req.Kind != commonv1.MediaKindSeries {
		return schema.TrailersResponse{Error: fmt.Sprintf("metadata: no trailers are listed for a %s", req.Kind)}
	}
	if len(s.providers) == 0 {
		return schema.TrailersResponse{Error: "metadata: no metadata provider is configured that lists trailers"}
	}
	videos := []trailers.Video{}
	var errs []error
	for _, p := range s.providers {
		pCtx, span := tracing.Start(ctx, "metadata.TrailerProvider.Trailers")
		got, err := p.Trailers(pCtx, req.Kind, pkgmetadata.ExternalIDs(req.IDs))
		if err != nil {
			tracing.RecordError(span, err)
			errs = append(errs, err)
		}
		span.End()
		videos = append(videos, got...)
	}
	if len(videos) == 0 && len(errs) > 0 {
		return schema.TrailersResponse{Error: fmt.Sprintf("metadata: list trailers: %v", errors.Join(errs...))}
	}
	if len(errs) > 0 {
		logging.FromContext(ctx).WarnContext(ctx, "metadata: a trailer provider could not be asked; answering the others' videos", "kind", req.Kind, "err", errors.Join(errs...))
	}
	return schema.TrailersResponse{Videos: videos}
}
