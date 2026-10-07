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

package segmenting

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The Applier reads each MediaFile uncached and applies its status.markers.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles/status,verbs=patch

// Options configures Setup.
type Options struct {
	Bus    events.Bus
	Reader client.Reader
	Client client.Client
}

// NewApplier is the status.markers writer both TheIntroDB's handler and the
// results use: reads through reader, applies through k8s.PatchStatus under
// catalogarr-markers.
func NewApplier(bus events.Bus, reader client.Reader, c client.Client) *Applier {
	return &Applier{Reader: reader, KV: bus.KV(events.BucketSegments), Apply: func(ctx context.Context, ac *catalogac.MediaFileApplyConfiguration) error {
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMarkers, ac)
		return err
	}}
}

// Setup subscribes catalogarr-segments-result, beside TheIntroDB's handler
// in the metadata gateway: both write status.markers through NewApplier.
// The planner is app/catalog/segmentplan.Setup.
func Setup(ctx context.Context, o Options) (stop func(), err error) {
	spec, ok := events.Default().Consumer(events.ConsumerCatalogSegmentsResult)
	if !ok {
		return nil, fmt.Errorf("segmenting: consumer %q missing from the default topology", events.ConsumerCatalogSegmentsResult)
	}
	r := &Results{Applier: NewApplier(o.Bus, o.Reader, o.Client)}
	stop, err = o.Bus.Subscribe(ctx, spec.Subscription(), r.Handle)
	if err != nil {
		return nil, fmt.Errorf("segmenting: subscribe %s: %w", events.ConsumerCatalogSegmentsResult, err)
	}
	return stop, nil
}
