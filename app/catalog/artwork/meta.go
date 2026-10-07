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
	"strconv"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
)

// The artwork object headers beyond Content-Type (artwork design §B.2), here
// so the light package both writers and the reaper import can build the
// whole set. The gateway and the renderer keep their own names as aliases.
const (
	// HeaderSource is where the bytes came from: provider, custom, or
	// render (an overlay).
	HeaderSource = "Clustarr-Source"

	// HeaderSourceURL is the URL the original was fetched from; an overlay
	// carries its original's.
	HeaderSourceURL = "Clustarr-Source-URL"

	// HeaderRenderedFrom is the inputs digest an overlay was rendered from.
	// An original never carries it.
	HeaderRenderedFrom = "Clustarr-Rendered-From"

	// SourceRender is an overlay's Clustarr-Source: neither the provider's
	// nor a custom image, but the renderer's.
	SourceRender = "render"
)

// ObjectRef is the identity of the item an artwork object belongs to.
type ObjectRef struct {
	Kind            commonv1.MediaKind
	UID             types.UID
	Namespace, Name string
}

// RefOf reads obj's identity as an ObjectRef of kind.
func RefOf(obj client.Object, kind commonv1.MediaKind) ObjectRef {
	return ObjectRef{Kind: kind, UID: obj.GetUID(), Namespace: obj.GetNamespace(), Name: obj.GetName()}
}

// OverlayFacts are what only an overlay carries.
type OverlayFacts struct {
	Profile        string // the winning OverlayProfile
	OriginalDigest string // hex digest of the original it was drawn on
	RenderedFrom   string // the inputs digest, header Clustarr-Rendered-From
}

// Facts are an artwork object's facts beyond its item, type and variant.
type Facts struct {
	ContentType   string
	Source        string // header Clustarr-Source: provider, custom, or render (an overlay)
	SourceURL     string // header Clustarr-Source-URL; an overlay carries its original's
	Language      string // ISO 639-1 of a provider image; "" leaves the key out
	Width, Height int    // pixels; zero or less leaves the key out
	Overlay       *OverlayFacts
}

// ObjectMeta is the complete header and metadata set of one artwork object
// (artwork design §B.2 as amended 2026-10-07). Every Put and SetMeta sends
// all of it: a Put that omits a key releases it (research E3).
//
// It always sets the version, kind, uid, namespace, name, image type and
// variant; width and height when positive; language when non-empty; and,
// for an overlay, its profile and original digest. The headers are
// Content-Type, Clustarr-Source, Clustarr-Source-URL when non-empty and, for
// an overlay, Clustarr-Rendered-From.
func ObjectMeta(ref ObjectRef, t catalogv1alpha1.ImageType, variant string, f Facts) events.ObjectMeta {
	headers := map[string]string{
		events.HeaderContentType: f.ContentType,
		HeaderSource:             f.Source,
	}
	if f.SourceURL != "" {
		headers[HeaderSourceURL] = f.SourceURL
	}
	meta := map[string]string{
		events.ArtworkMetaKeyVersion:   events.ArtworkMetaVersion,
		events.ArtworkMetaKeyKind:      string(ref.Kind),
		events.ArtworkMetaKeyUID:       string(ref.UID),
		events.ArtworkMetaKeyNamespace: ref.Namespace,
		events.ArtworkMetaKeyName:      ref.Name,
		events.ArtworkMetaKeyImageType: string(t),
		events.ArtworkMetaKeyVariant:   variant,
	}
	if f.Width > 0 {
		meta[events.ArtworkMetaKeyWidth] = strconv.Itoa(f.Width)
	}
	if f.Height > 0 {
		meta[events.ArtworkMetaKeyHeight] = strconv.Itoa(f.Height)
	}
	if f.Language != "" {
		meta[events.ArtworkMetaKeyLanguage] = f.Language
	}
	if o := f.Overlay; o != nil {
		headers[HeaderRenderedFrom] = o.RenderedFrom
		meta[events.ArtworkMetaKeyProfile] = o.Profile
		meta[events.ArtworkMetaKeyOriginalDigest] = o.OriginalDigest
	}
	return events.ObjectMeta{Headers: headers, Metadata: meta}
}

// MetaCurrent reports whether info's metadata is at
// events.ArtworkMetaVersion: an object stored before the set existed, or
// under an older version, is backfilled by its writer.
func MetaCurrent(info events.ObjectInfo) bool {
	return info.Metadata[events.ArtworkMetaKeyVersion] == events.ArtworkMetaVersion
}
