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
	"fmt"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// ServeRPC registers the gateway's three request/reply methods --
// rpc.catalogarr.metadata.{lookup,search,resolve} -- in the catalogarr
// queue group. Unlike Handler, these never touch a Kubernetes object: the
// caller (an import list, an interactive lookup) supplies ids and gets a
// provider document back.
func ServeRPC(bus events.Requester, reg *pkgmetadata.Registry) error {
	handlers := map[string]func(context.Context, schema.MetadataRequest) schema.MetadataResponse{
		events.RPCMetadataLookup: func(ctx context.Context, req schema.MetadataRequest) schema.MetadataResponse {
			return lookup(ctx, reg, req)
		},
	}
	for subject, h := range handlers {
		h := h
		err := bus.Serve(subject, events.QueueGroupCatalogar, func(ctx context.Context, data []byte) ([]byte, error) {
			var req schema.MetadataRequest
			if err := json.Unmarshal(data, &req); err != nil {
				return nil, fmt.Errorf("metadata: decode MetadataRequest: %w", err)
			}
			return json.Marshal(h(ctx, req))
		})
		if err != nil {
			return fmt.Errorf("metadata: serve %s: %w", subject, err)
		}
	}
	return nil
}

func lookup(ctx context.Context, reg *pkgmetadata.Registry, req schema.MetadataRequest) schema.MetadataResponse {
	v, err := reg.Lookup(ctx, req.Kind, req.IDs)
	if err != nil {
		return schema.MetadataResponse{Kind: req.Kind, Error: err.Error()}
	}
	result, err := json.Marshal(v)
	if err != nil {
		return schema.MetadataResponse{Kind: req.Kind, Error: err.Error()}
	}
	return schema.MetadataResponse{Kind: req.Kind, IDs: idsOf(v), Result: result}
}

func idsOf(v any) map[string]string {
	switch e := v.(type) {
	case *pkgmetadata.Movie:
		return e.IDs
	case *pkgmetadata.Series:
		return e.IDs
	case *pkgmetadata.Artist:
		return e.IDs
	case *pkgmetadata.Author:
		return e.IDs
	case *pkgmetadata.Audiobook:
		return e.IDs
	case *pkgmetadata.ComicVolume:
		return e.IDs
	default:
		return nil
	}
}
