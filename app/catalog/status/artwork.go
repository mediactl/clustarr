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

// Package status declares which catalogarr field manager owns which leaves
// of a catalog item's status where more than one catalogarr writer shares an
// object, restated as data so a managedFields test can hold each manager to
// its set -- the pattern app/grab/status, app/squash/status and
// app/caption/status follow for their kinds.
//
// It starts with the artwork split of spec §B.3/§B.6. On Movie, Series,
// Artist, Album, Author, Book, Audiobook and Comic:
//
//   - [GatewayManager] (the metadata gateway) owns status.metadata -- every
//     leaf but Album's metadata.selectedReleaseID, which is the Album
//     reconciler's under k8s.ManagerCatalogarr -- and all of
//     status.artwork, and declares both in ONE apply ([GatewayFields]);
//   - [RendererManager] (the artwork render role, catalogarr --role
//     artwork) owns status.overlay on Movie and Series and nothing else
//     ([RendererFields]); [PatchOverlay] is its one write, a complete
//     declaration of all four leaves or of none.
//
// An over-claim is silent (CLAUDE.md): ForceOwnership lets the later
// applier take a field without a conflict, so a split is proved only by
// reading metadata.managedFields. [OwnedStatusPaths] reads it.
package status

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// GatewayManager is the metadata gateway's field manager, the one that
// already wrote each kind's status.metadata before artwork existed (spec
// §B.6: "written by the gateway under the manager that already writes that
// kind's status.metadata").
const GatewayManager = k8s.ManagerCatalogarrMetadata

// GatewayFields are the top-level status fields GatewayManager owns on each
// of the eight kinds with artwork. Every gateway apply -- the metadata
// work-queue handler's and the artwork-fetch handler's alike -- declares all
// of them, because server-side apply releases whatever a manager's apply
// omits: an apply of metadata alone would delete every artwork entry.
var GatewayFields = []string{"artwork", "metadata"}

// ArtworkEntryLeaves are the leaves of one status.artwork entry. All six
// are required by the CRD and [ArtworkEntries] sends every one of them on
// every apply; SSA tracks ownership per leaf, so a renderer that sent five
// would release the sixth.
var ArtworkEntryLeaves = []string{"digest", "sizeBytes", "source", "sourceURL", "type", "updatedAt"}

// ArtworkEntries renders status.artwork's apply configuration: one entry
// per element, every leaf set. It is the only renderer of the list, so a
// caller passes its result to exactly one With Artwork call -- the
// generated With* list builders append, and a second call would double
// every entry.
func ArtworkEntries(entries []catalogv1alpha1.ArtworkEntry) []*catalogac.ArtworkEntryApplyConfiguration {
	out := make([]*catalogac.ArtworkEntryApplyConfiguration, 0, len(entries))
	for _, e := range entries {
		out = append(out, catalogac.ArtworkEntry().
			WithType(e.Type).
			WithSource(e.Source).
			WithSourceURL(e.SourceURL).
			WithDigest(e.Digest).
			WithSizeBytes(e.SizeBytes).
			WithUpdatedAt(e.UpdatedAt))
	}
	return out
}

// RendererManager is the artwork render role's field manager (spec §B.6:
// "written by the renderer under a new manager k8s.ManagerCatalogarrArtwork").
const RendererManager = k8s.ManagerCatalogarrArtwork

// RendererFields are the top-level status fields RendererManager owns, on
// Movie and Series only. Disjoint from [GatewayFields]: the renderer never
// reads, writes or releases status.artwork, and the gateway never declares
// status.overlay (spec §B.3).
var RendererFields = []string{"overlay"}

// OverlayEntryLeaves are the leaves of status.overlay. All four are required
// by the CRD and [OverlayEntryAC] sends every one on every apply; SSA tracks
// ownership per leaf, so an apply that sent three would release the fourth.
var OverlayEntryLeaves = []string{"digest", "profileRef", "renderedFrom", "updatedAt"}

// ErrNotTheRenderer is a [PatchOverlay] by any manager but [RendererManager].
var ErrNotTheRenderer = errors.New("status: only the renderer (catalogarr-artwork) writes status.overlay")

// ErrNoOverlay is a [PatchOverlay] of a kind with no status.overlay --
// anything but Movie and Series.
var ErrNoOverlay = errors.New("status: kind has no status.overlay")

// HasOverlay reports whether kind carries status.overlay: Movie and Series
// (spec §B.6). It is the one statement of that rule -- the gateway asks it
// before publishing a render task, the renderer before drawing one
// (app/catalog/worker/artwork.Overlaid) -- because two copies had drifted:
// the gateway published a task for every kind with a poster, the renderer
// discarded the non-video ones, and each discard was dead-lettered.
func HasOverlay(kind commonv1.MediaKind) bool {
	return kind == commonv1.MediaKindMovie || kind == commonv1.MediaKindSeries
}

// OverlayEntryAC renders status.overlay's apply configuration with every
// leaf set.
func OverlayEntryAC(e catalogv1alpha1.OverlayEntry) *catalogac.OverlayEntryApplyConfiguration {
	return catalogac.OverlayEntry().
		WithProfileRef(e.ProfileRef).
		WithDigest(e.Digest).
		WithRenderedFrom(e.RenderedFrom).
		WithUpdatedAt(e.UpdatedAt)
}

// PatchOverlay is the renderer's one status write: a single apply under
// mgr, which must be [RendererManager], declaring exactly status.overlay --
// entry's four leaves, or, with a nil entry, nothing at all, which releases
// every overlay leaf the renderer owned and so removes status.overlay.
//
// The apply is built from the object's name and namespace alone and carries
// no other status field, so it can neither release nor co-own the
// gateway's status.metadata and status.artwork, nor any reconciler's leaf.
// It refuses any other manager -- the grab path, the gateway and the item
// reconcilers all write Movie.status, and a stray call from one of them
// would silently co-own status.overlay (ForceOwnership) -- and any kind
// but Movie and Series, before touching c.
func PatchOverlay(ctx context.Context, c client.Client, mgr k8s.FieldManager, obj client.Object,
	entry *catalogv1alpha1.OverlayEntry,
) error {
	if mgr != RendererManager {
		return fmt.Errorf("%w: refused %q", ErrNotTheRenderer, mgr)
	}
	var ac k8s.ApplyConfiguration
	switch obj.(type) {
	case *catalogv1alpha1.Movie:
		st := catalogac.MovieStatus()
		if entry != nil {
			st.WithOverlay(OverlayEntryAC(*entry))
		}
		ac = catalogac.Movie(obj.GetName(), obj.GetNamespace()).WithStatus(st)
	case *catalogv1alpha1.Series:
		st := catalogac.SeriesStatus()
		if entry != nil {
			st.WithOverlay(OverlayEntryAC(*entry))
		}
		ac = catalogac.Series(obj.GetName(), obj.GetNamespace()).WithStatus(st)
	default:
		return fmt.Errorf("%w: %T", ErrNoOverlay, obj)
	}
	_, err := k8s.PatchStatus(ctx, c, mgr, ac)
	return err
}

// OwnedStatusPaths returns every status leaf manager owns through the
// status subresource, as a path relative to status: "metadata.title",
// "artwork[type=poster].digest". A list-map entry's key renders as
// [k=v,...] sorted by key.
func OwnedStatusPaths(managed []metav1.ManagedFieldsEntry, manager k8s.FieldManager) (sets.Set[string], error) {
	out := sets.New[string]()
	for _, e := range managed {
		if e.Manager != string(manager) || e.Subresource != "status" || e.FieldsV1 == nil {
			continue
		}
		var root map[string]any
		if err := json.Unmarshal(e.FieldsV1.GetRawBytes(), &root); err != nil {
			return nil, fmt.Errorf("status: decode %s's managed fields: %w", manager, err)
		}
		st, _ := root["f:status"].(map[string]any)
		if err := walkFields(st, "", out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TopLevel returns the distinct first segment of each path:
// "artwork[type=poster].digest" -> "artwork".
func TopLevel(paths sets.Set[string]) sets.Set[string] {
	out := sets.New[string]()
	for p := range paths {
		out.Insert(strings.FieldsFunc(p, func(r rune) bool { return r == '.' || r == '[' })[0])
	}
	return out
}

func walkFields(node map[string]any, prefix string, out sets.Set[string]) error {
	for k, v := range node {
		if k == "." {
			continue
		}
		var path string
		switch {
		case strings.HasPrefix(k, "f:"):
			path = strings.TrimPrefix(k, "f:")
			if prefix != "" {
				path = prefix + "." + path
			}
		case strings.HasPrefix(k, "k:"):
			var key map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(k, "k:")), &key); err != nil {
				return fmt.Errorf("status: decode list key %q: %w", k, err)
			}
			parts := make([]string, 0, len(key))
			for name, val := range key {
				parts = append(parts, fmt.Sprintf("%s=%v", name, val))
			}
			sort.Strings(parts)
			path = prefix + "[" + strings.Join(parts, ",") + "]"
		default: // "v:" set members and "i:" indices
			path = prefix + "[" + k + "]"
		}
		child, _ := v.(map[string]any)
		leaf := true
		for ck := range child {
			if ck != "." {
				leaf = false
				break
			}
		}
		if leaf {
			out.Insert(path)
			continue
		}
		if err := walkFields(child, path, out); err != nil {
			return err
		}
	}
	return nil
}
