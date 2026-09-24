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
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/version"
)

// Drift compares the sources a pass would store against status.artwork
// (spec §B.7). The sources are ResolveSources' -- each type's override, else
// the first fetchable provider image in status.metadata.images -- the very
// map Fetcher.Sync fetches from, so drift is never something a pass would
// not act on. It reports drift when any type's source differs from its
// entry's sourceURL or source (no entry counts as different), or when an
// entry says custom but no override of that type exists any more. A
// provider entry whose image the metadata no longer lists is not drift:
// Sync keeps it.
//
// Provider images are sources as much as overrides are. An item whose
// metadata was fetched before the gateway stored artwork has images and
// no entries, and the metadata TTL (7 days for a released movie, 30 for an
// ended series) may not bring another fetch for weeks; a provider image
// whose fetch failed keeps its old entry, or none. Both drift here, so the
// reconciler's fetch task stores them from the stored images without
// refetching metadata.
//
// specHash is the hex SHA-256 of the resolved sources sorted by type,
// whether or not they drifted: the Msg-Id suffix that lets a hot reconcile
// loop publish one fetch per set of sources (schema.MsgIDForArtworkFetch),
// and gives a changed override or provider URL a fetch of its own.
//
// A source that keeps failing keeps drifting -- R3 leaves its entry as it
// was -- and so is republished once per duplicate window at most, on
// whatever reconcile next sees it.
func Drift(overrides []catalogv1alpha1.ArtworkOverride, images []catalogv1alpha1.Image,
	entries []catalogv1alpha1.ArtworkEntry,
) (specHash string, drifted bool) {
	sources := ResolveSources(overrides, images)
	byType := index(entries)
	h := sha256.New()
	for _, t := range imageTypes {
		src, ok := sources[t]
		if !ok {
			continue
		}
		// NUL-separated: neither an enum token nor a URL admits one, so no
		// two source sets share a byte stream.
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00", t, src.Kind, src.URL)
		if e, ok := byType[t]; !ok || e.SourceURL != src.URL || e.Source != src.Kind {
			drifted = true
		}
	}
	specHash = hex.EncodeToString(h.Sum(nil))

	for _, e := range entries {
		if e.Source == catalogv1alpha1.ArtworkSourceCustom && sources[e.Type].Kind != catalogv1alpha1.ArtworkSourceCustom {
			drifted = true
		}
	}
	return specHash, drifted
}

// PublishFetch is the one call each of the eight item reconcilers makes: when
// obj's artwork sources (spec.artwork and status.metadata.images, see
// [Drift]) have drifted from its status.artwork it publishes one
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
	specHash, drifted := Drift(it.overrides, it.images, it.entries)
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

// publishRender publishes one RenderOverlay task (spec §B.4) under
// schema.MsgIDForRenderOverlay(uid, posterDigest) -- posterDigest being
// RenderNoPoster when the item has no poster original. See
// Pass.publishRenders for when.
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
