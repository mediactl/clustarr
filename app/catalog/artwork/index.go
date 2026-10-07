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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/objindex"
)

// EntryOf is the status.artwork entry an original object's index entry
// describes (ADR-0019 D13): its type, the source and source URL its
// metadata records, its digest and size, and when it was stored. false when
// the object's metadata is below events.ArtworkMetaVersion, or lacks a key
// the entry needs -- its writer backfills it when the item key asks.
func EntryOf(t catalogv1alpha1.ImageType, e objindex.Entry) (catalogv1alpha1.ArtworkEntry, bool) {
	m := e.Metadata
	if !MetaCurrentMap(m) {
		return catalogv1alpha1.ArtworkEntry{}, false
	}
	src := catalogv1alpha1.ArtworkSource(m[events.ArtworkMetaKeySource])
	if src != catalogv1alpha1.ArtworkSourceProvider && src != catalogv1alpha1.ArtworkSourceCustom {
		return catalogv1alpha1.ArtworkEntry{}, false
	}
	url := m[events.ArtworkMetaKeySourceURL]
	at, ok := ParseUpdatedAt(m)
	if url == "" || !ok || e.Digest == "" {
		return catalogv1alpha1.ArtworkEntry{}, false
	}
	return catalogv1alpha1.ArtworkEntry{
		Type: t, Source: src, SourceURL: url, Digest: e.Digest, SizeBytes: e.Size, UpdatedAt: metav1.NewTime(at),
	}, true
}

// EntriesFromIndex is status.artwork as the index's original objects of the
// item describe it, in the ImageType enum's order: only objects whose
// metadata is current (EntryOf). A lost bucket reads as no artwork at once.
func EntriesFromIndex(idx *objindex.Index, kind commonv1.MediaKind, uid types.UID) []catalogv1alpha1.ArtworkEntry {
	var out []catalogv1alpha1.ArtworkEntry
	for _, t := range ImageTypes {
		e, ok := idx.Lookup(events.ArtworkKey(kind, uid, string(t), events.ArtworkVariantOriginal))
		if !ok {
			continue
		}
		if entry, ok := EntryOf(t, e); ok {
			out = append(out, entry)
		}
	}
	return out
}

// Entries is obj's status.artwork as the manager renders it (ADR-0019
// §7.1): EntriesFromIndex, except that a type whose object stands but whose
// metadata predates events.ArtworkMetaVersion keeps the entry status
// already records until its backfill lands -- the index cannot describe it,
// which is not the same as its being gone. false while the index has not
// synced: then nothing is known and status stands as it is.
func Entries(idx *objindex.Index, obj client.Object) ([]catalogv1alpha1.ArtworkEntry, bool) {
	if idx == nil || !idx.Synced() {
		return nil, false
	}
	it, err := ItemOf(obj)
	if err != nil {
		return nil, false
	}
	stored := Index(it.Entries)
	var out []catalogv1alpha1.ArtworkEntry
	for _, t := range ImageTypes {
		e, ok := idx.Lookup(events.ArtworkKey(it.Kind, obj.GetUID(), string(t), events.ArtworkVariantOriginal))
		if !ok {
			continue
		}
		if entry, ok := EntryOf(t, e); ok {
			out = append(out, entry)
			continue
		}
		if s, ok := stored[t]; ok {
			out = append(out, s)
		}
	}
	return out, true
}

// Object is what the index knows of one type's original, for the fetch
// plan.
type Object struct {
	Present bool
	// Current reports metadata at events.ArtworkMetaVersion; the fields
	// below are read only then.
	Current   bool
	Source    catalogv1alpha1.ArtworkSource
	SourceURL string
	Digest    string
	UpdatedAt metav1.Time
}

// ObjectsFromIndex is what the index knows of each type's original of the
// item.
func ObjectsFromIndex(idx *objindex.Index, kind commonv1.MediaKind, uid types.UID) map[catalogv1alpha1.ImageType]Object {
	out := map[catalogv1alpha1.ImageType]Object{}
	for _, t := range ImageTypes {
		e, ok := idx.Lookup(events.ArtworkKey(kind, uid, string(t), events.ArtworkVariantOriginal))
		if !ok {
			continue
		}
		o := Object{Present: true, Digest: e.Digest}
		if entry, ok := EntryOf(t, e); ok {
			o.Current, o.Source, o.SourceURL, o.UpdatedAt = true, entry.Source, entry.SourceURL, entry.UpdatedAt
		}
		out[t] = o
	}
	return out
}

// OverlayOf is status.overlay as the item's overlay object describes it
// (ADR-0019 §7.2): its profile, digest, inputs digest and update time. The
// entry is the index's, ok false when there is no overlay object; current
// false when its metadata predates events.ArtworkMetaVersion (the renderer
// backfills it), and the overlay is then nil.
func OverlayOf(idx *objindex.Index, kind commonv1.MediaKind, uid types.UID) (overlay *catalogv1alpha1.OverlayEntry, e objindex.Entry, ok, current bool) {
	e, ok = idx.Lookup(events.ArtworkKey(kind, uid, string(catalogv1alpha1.ImageTypePoster), events.ArtworkVariantOverlay))
	if !ok {
		return nil, e, false, false
	}
	m := e.Metadata
	at, hasAt := ParseUpdatedAt(m)
	if !MetaCurrentMap(m) || !hasAt || m[events.ArtworkMetaKeyProfile] == "" || m[events.ArtworkMetaKeyRenderedFrom] == "" {
		return nil, e, true, false
	}
	return &catalogv1alpha1.OverlayEntry{
		ProfileRef:   m[events.ArtworkMetaKeyProfile],
		Digest:       e.Digest,
		RenderedFrom: m[events.ArtworkMetaKeyRenderedFrom],
		UpdatedAt:    metav1.NewTime(at),
	}, e, true, true
}

// ItemOfMeta names the item an artwork object belongs to, from its
// metadata: its media kind, namespace and name. false for an object whose
// metadata names none (stored before object metadata existed).
func ItemOfMeta(m map[string]string) (kind commonv1.MediaKind, namespace, name string, ok bool) {
	kind = commonv1.MediaKind(m[events.ArtworkMetaKeyKind])
	namespace, name = m[events.ArtworkMetaKeyNamespace], m[events.ArtworkMetaKeyName]
	return kind, namespace, name, kind != "" && name != ""
}
