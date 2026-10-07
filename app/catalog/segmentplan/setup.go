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

package segmentplan

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/events"
)

// The Planner lists a season's episodes and their files through the cache.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles;episodes;series,verbs=get;list;watch

// Options configures Setup.
type Options struct {
	Bus events.Bus
	// Reader must be a cache with the Episode-by-series and
	// MediaFile-by-episode indexes: the controller role's.
	Reader client.Reader
}

// Setup subscribes catalogarr-segments-plan to a Planner. Results are
// clustarr-segments records cmd/markers writes; the remediation loop's
// markers planner merges them (loop spec §4.12).
func Setup(ctx context.Context, o Options) (stop func(), err error) {
	spec, ok := events.Default().Consumer(events.ConsumerCatalogSegmentsPlan)
	if !ok {
		return nil, fmt.Errorf("segmenting: consumer %q missing from the default topology", events.ConsumerCatalogSegmentsPlan)
	}
	stop, err = o.Bus.Subscribe(ctx, spec.Subscription(), (&Planner{Reader: o.Reader, Bus: o.Bus}).Handle)
	if err != nil {
		return nil, fmt.Errorf("segmenting: subscribe %s: %w", events.ConsumerCatalogSegmentsPlan, err)
	}
	return stop, nil
}
