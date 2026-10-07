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

package artwork

import (
	"context"
	"math"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/app/catalog/overlayplan"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// RenderDurable is the consumer a render task is admitted on (R23).
const RenderDurable = events.ConsumerCatalogArtworkRender

// OverlayStage is the overlay half of the artwork stage (ADR-0019 §7.2,
// A5.3; remediation.ItemStage), Movie and Series: it plans the item's
// overlay (overlayplan.Plan over the item's labels and ratings, its
// namespace's OverlayProfiles and the poster original's digest in the
// index), publishes a render task when the overlay object's metadata does
// not record that plan, and renders status.overlay from that metadata under
// catalog-artwork. W4.82's status.overlay compare-and-swap is superseded: the
// renderer writes the object only, and this stage is status.overlay's one
// writer, one apply per pass.
type OverlayStage struct{ o Options }

var _ remediation.ItemStage = (*OverlayStage)(nil)

// NewOverlay builds the overlay stage over the same index and ledger as the
// fetch stage.
func NewOverlay(o Options) *OverlayStage { return &OverlayStage{o: o} }

// Name implements remediation.ItemStage.
func (s *OverlayStage) Name() remediation.ItemStageName { return remediation.StageOverlay }

// Applies implements remediation.ItemStage: the two kinds with an overlay.
func (s *OverlayStage) Applies(k remediation.KeyKind) bool {
	return k == remediation.KindMovie || k == remediation.KindSeries
}

// Plan implements remediation.ItemStage.
func (s *OverlayStage) Plan(ctx context.Context, env *remediation.Env, v *remediation.ItemView) (remediation.ItemResult, error) {
	now := v.Now.Time
	if s.o.Index == nil || !s.o.Index.Synced() {
		return remediation.ItemResult{Due: now.Add(indexWait)}, nil
	}
	it, err := overlayplan.ItemOf(v.Item)
	if err != nil {
		return remediation.ItemResult{}, nil
	}
	var profiles catalogv1alpha1.OverlayProfileList
	if err := env.Reader.List(ctx, &profiles, client.InNamespace(v.Item.GetNamespace())); err != nil {
		return remediation.ItemResult{}, remediation.Transient(err)
	}
	uid := v.Item.GetUID()
	poster := ""
	if e, ok := s.o.Index.Lookup(events.ArtworkKey(it.Kind, uid, string(catalogv1alpha1.ImageTypePoster), events.ArtworkVariantOriginal)); ok {
		poster = e.Digest
	}
	want := overlayplan.Plan(it, profiles.Items, poster)
	recorded, _, present, current := catalogartwork.OverlayOf(s.o.Index, it.Kind, uid)

	// status.overlay is what the overlay object's metadata says while the
	// item wants an overlay, and none when it wants none (the render task
	// below removes the object). An object stored under an older metadata
	// version keeps what status records until the renderer backfills it.
	status := recorded
	switch {
	case want.Profile == nil:
		status = nil
	case present && !current:
		status = it.Overlay
	}
	var res remediation.ItemResult
	res.Sets = []itempass.Set{{Manager: k8s.ManagerCatalogArtwork, Fields: map[string]any{"overlay": overlayValue(status)}}}

	render := false
	token := overlayplan.RenderNone
	if want.Profile == nil {
		render = present
	} else {
		token = want.InputsDigest
		render = !present || !current || recorded.RenderedFrom != want.InputsDigest || recorded.ProfileRef != want.ProfileName()
	}
	key := remediation.ItemLedgerKey(v.Key)
	if !render || v.Item.GetDeletionTimestamp() != nil {
		res.Effects = append(res.Effects, remediation.DispatchAnswered{Durable: RenderDurable, Ledger: key, Seq: math.MaxInt64})
		return res, nil
	}
	seq := now.Unix()
	if s.o.Dispatch != nil {
		d := s.o.Dispatch.Admit(RenderDurable, key, seq)
		if !d.Admitted {
			s.o.Dispatch.Waiting(RenderDurable, key, d.Reason)
			res.Due = now.Add(max(d.RetryAfter, time.Second))
			return res, nil
		}
	}
	subject, env2, err := overlayplan.Task(it, token, overlayplan.ReasonPlan, now)
	if err != nil {
		return remediation.ItemResult{}, err
	}
	res.Effects = append(res.Effects, remediation.DispatchPublish{
		Publish: remediation.Publish{Subject: subject, MsgID: env2.ID, ExpectStream: events.StreamWorkCatalog, Envelope: env2},
		Durable: RenderDurable, Ledger: key, Seq: seq,
	})
	return res, nil
}

// overlayValue is status.overlay as a set's field: nil sends it absent,
// which releases it.
func overlayValue(o *catalogv1alpha1.OverlayEntry) any {
	if o == nil {
		return nil
	}
	return o
}
