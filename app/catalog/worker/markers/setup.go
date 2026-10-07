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

package markers

import (
	"context"
	"fmt"

	catalogmarkers "github.com/mediactl/clustarr/app/catalog/markers"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// Options configures Setup.
type Options struct {
	Bus    events.Bus
	Writer string // the pod's name, stamped on every answer (POD_NAME)
}

// Setup subscribes the catalogarr-markers durable to a Handler asking
// providers, the metadata gateway's registry's Markers. The gateway calls
// it once it has built the registry (catalogmetadata.Options.Markers).
func Setup(ctx context.Context, o Options, providers []metadata.MarkersProvider) (stop func(), err error) {
	spec, ok := events.Default().Consumer(events.ConsumerCatalogMarkers)
	if !ok {
		return nil, fmt.Errorf("markers: consumer %q missing from the default topology", events.ConsumerCatalogMarkers)
	}
	h := &Handler{
		Providers: providers, Bus: o.Bus, Answers: catalogmarkers.NewAnswers(o.Bus, o.Writer),
		MaxDeliver: uint64(max(spec.MaxDeliver, 0)),
	}
	stop, err = o.Bus.Subscribe(ctx, spec.Subscription(), h.Handle)
	if err != nil {
		return nil, fmt.Errorf("markers: subscribe %s: %w", events.ConsumerCatalogMarkers, err)
	}
	return stop, nil
}
