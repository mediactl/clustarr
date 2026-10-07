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

package mfindex

import (
	"context"
	"fmt"
	"slices"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Item is the one index of MediaFiles by the catalog item they back (loop
// spec §3.16). It replaces nine: the six item controllers'
// ".spec.mediaRef.<kind>", the MediaFile reconciler's
// "mediafile.clustarr.io/owner", captionarr's "captionarr.spec.mediaRef" and
// librarydelete's "librarydelete.spec.mediaRef.target". Readers: the item
// keys, the loop's naming wakes, librarydelete, the segment planner, and
// until F5.3 the two caption controllers.
const Item = "remediation.mediafile.item"

// Index declares Item (split spec §3.5.2). The remediation loop registers it
// once per process, through Register from remediation.RegisterIndexes; every
// other reader only lists through it, and a test that runs a reader without
// the loop registers it itself.
var Index = k8s.FieldIndex{Object: &catalogv1alpha1.MediaFile{}, Name: Item, Extract: Extract}

// ItemKey is Item's value for one item: "<kind>/<name>", kind in the
// MediaRef vocabulary ("movie", "episode", ...).
func ItemKey(kind commonv1.MediaKind, name string) string { return string(kind) + "/" + name }

// Extract is Item's extractor: the ItemKey of spec.mediaRef's item and of
// every spec.mediaRef.keys entry, each once, so every episode of a
// multi-episode file (and every issue of a pack) finds it. mediaRef.track
// narrows an Album reference and adds no value. A file attributed to
// nothing has none.
func Extract(o client.Object) []string {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok || mf.Spec.MediaRef.Kind == "" || mf.Spec.MediaRef.Name == "" {
		return nil
	}
	ref := mf.Spec.MediaRef
	out := []string{ItemKey(ref.Kind, ref.Name)}
	for _, name := range ref.Keys {
		if key := ItemKey(ref.Kind, name); name != "" && !slices.Contains(out, key) {
			out = append(out, key)
		}
	}
	return out
}

// FilesOf lists the MediaFiles of the item kind/name in namespace through
// Item, never the whole namespace (CLAUDE.md, "A watch's map function runs
// for every object at startup"). opts are added to the List, so a map
// function that only reads names passes client.UnsafeDisableDeepCopy.
func FilesOf(ctx context.Context, r client.Reader, namespace string, kind commonv1.MediaKind, name string,
	opts ...client.ListOption,
) ([]catalogv1alpha1.MediaFile, error) {
	var files catalogv1alpha1.MediaFileList
	all := append([]client.ListOption{client.InNamespace(namespace), client.MatchingFields{Item: ItemKey(kind, name)}}, opts...)
	if err := r.List(ctx, &files, all...); err != nil {
		return nil, fmt.Errorf("mfindex: MediaFiles of %s %s/%s: %w", kind, namespace, name, err)
	}
	return files.Items, nil
}
