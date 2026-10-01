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
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/segments"
)

// Results is the catalogarr-segments-result durable's handler: it records a
// worker's result for one file and merges it into status.markers.
type Results struct {
	Applier *Applier
	Clock   func() time.Time
}

// Handle implements events.Handler.
func (r *Results) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	var res schema.SegmentsResult
	if err := schema.Decode(env.Schema, env.Data, &res); err != nil {
		return events.Discard("undecodable segments result", err)
	}
	ns, name, ok := strings.Cut(env.Key, "/")
	if !ok || name == "" {
		return events.Discard("envelope key is not <namespace>/<name>", fmt.Errorf("key=%q", env.Key))
	}
	now := time.Now()
	if r.Clock != nil {
		now = r.Clock()
	}
	up := &AnalysisUpdate{
		Result: catalogv1alpha1.MarkersResult(res.Result), AnalyzedAt: metav1.NewTime(now),
		ForProbeHash: res.ProbeHash, Version: res.Version, Message: res.Message,
	}
	for _, s := range res.Segments {
		up.Segments = append(up.Segments, segments.Segment{
			Kind: catalogv1alpha1.MarkerKind(s.Kind), StartMs: s.StartMs, EndMs: s.EndMs,
			Source: catalogv1alpha1.SegmentSource(s.Source), Confidence: s.Confidence,
		})
	}
	return r.Applier.ApplyMerged(ctx, client.ObjectKey{Namespace: ns, Name: name}, nil, up)
}
