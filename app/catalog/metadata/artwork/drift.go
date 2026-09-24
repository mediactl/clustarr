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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/version"
)

// Drift compares spec.artwork against status.artwork (spec §B.7). It
// reports drift when any override's URL differs from its type's entry's
// sourceURL (no entry counts as different), or when an entry says custom
// but no override of that type exists any more.
//
// specHash is the hex SHA-256 of spec.artwork sorted by type, whether or
// not it drifted: the Msg-Id suffix that lets a hot reconcile loop publish
// one fetch per spec (schema.MsgIDForArtworkFetch).
//
// A custom URL that keeps failing keeps drifting -- R3 leaves its entry as
// it was -- and so is republished once per duplicate window at most, on
// whatever reconcile next sees it.
func Drift(overrides []catalogv1alpha1.ArtworkOverride, entries []catalogv1alpha1.ArtworkEntry) (specHash string, drifted bool) {
	sorted := append([]catalogv1alpha1.ArtworkOverride(nil), overrides...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Type < sorted[j].Type })
	h := sha256.New()
	for _, o := range sorted {
		// NUL-separated: neither an enum token nor a URL admits one, so no
		// two specs share a byte stream.
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00", o.Type, o.URL)
	}
	specHash = hex.EncodeToString(h.Sum(nil))

	byType := index(entries)
	wanted := make(map[catalogv1alpha1.ImageType]bool, len(overrides))
	for _, o := range overrides {
		wanted[o.Type] = true
		if e, ok := byType[o.Type]; !ok || e.SourceURL != o.URL {
			drifted = true
		}
	}
	for _, e := range entries {
		if e.Source == catalogv1alpha1.ArtworkSourceCustom && !wanted[e.Type] {
			drifted = true
		}
	}
	return specHash, drifted
}

// PublishFetch is the one call each of the eight item reconcilers makes: when
// obj's spec.artwork has drifted from its status.artwork it publishes one
// schema.ArtworkFetchTask on events.WorkArtworkFetchSubject, for the
// gateway's catalogarr-artwork-fetch durable, under
// schema.MsgIDForArtworkFetch(uid, specHash) -- so a reconcile loop that
// keeps seeing the same drift publishes once per duplicate window. Without
// drift it publishes nothing.
//
// kind must be obj's own kind; anything else, or an object with no artwork,
// is an error rather than a guess.
func PublishFetch(ctx context.Context, bus events.Publisher, obj client.Object, kind commonv1.MediaKind) error {
	it, err := itemOf(obj)
	if err != nil {
		return err
	}
	if it.kind != kind {
		return fmt.Errorf("artwork: PublishFetch for kind %q given a %T", kind, obj)
	}
	specHash, drifted := Drift(it.overrides, it.entries)
	if !drifted {
		return nil
	}
	if bus == nil {
		return fmt.Errorf("artwork: %s %s/%s drifted but there is no bus to publish on", kind, obj.GetNamespace(), obj.GetName())
	}
	schemaName, data, err := schema.Encode(schema.ArtworkFetchTask{
		MediaRef: commonv1.MediaRef{Kind: kind, Name: obj.GetName()},
	})
	if err != nil {
		return err
	}
	env := &events.Envelope{
		ID:     schema.MsgIDForArtworkFetch(obj.GetUID(), specHash),
		Type:   "catalog.ArtworkFetchTask",
		Schema: schemaName,
		Source: "catalogarr@" + version.String(),
		// The <namespace>/<name> routing key every catalogarr worker parses;
		// the subject carries the tokenised media key instead.
		Key:  obj.GetNamespace() + "/" + obj.GetName(),
		Time: time.Now().UTC(),
		Data: data,
	}
	mediaKey := events.MediaKey(string(kind), obj.GetNamespace(), obj.GetName())
	if _, err := bus.Publish(ctx, events.WorkArtworkFetchSubject(mediaKey), env); err != nil {
		return fmt.Errorf("artwork: publish the fetch task: %w", err)
	}
	return nil
}

// publishRender publishes the RenderOverlay task spec §B.4 asks for after a
// poster original whose digest changed is recorded, under
// schema.MsgIDForRenderOverlay(uid, posterDigest).
func publishRender(ctx context.Context, bus events.Publisher, obj client.Object, kind commonv1.MediaKind, posterDigest string) error {
	schemaName, data, err := schema.Encode(schema.RenderOverlayTask{
		MediaRef: commonv1.MediaRef{Kind: kind, Name: obj.GetName()},
		Reason:   "original",
	})
	if err != nil {
		return err
	}
	env := &events.Envelope{
		ID:     schema.MsgIDForRenderOverlay(obj.GetUID(), posterDigest),
		Type:   "catalog.RenderOverlayTask",
		Schema: schemaName,
		Source: "catalogarr@" + version.String(),
		Key:    obj.GetNamespace() + "/" + obj.GetName(),
		Time:   time.Now().UTC(),
		Data:   data,
	}
	mediaKey := events.MediaKey(string(kind), obj.GetNamespace(), obj.GetName())
	if _, err := bus.Publish(ctx, events.WorkArtworkRenderSubject(mediaKey), env); err != nil {
		return fmt.Errorf("artwork: publish the render task: %w", err)
	}
	return nil
}
