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

package audiobook

import (
	"context"
	"encoding/json"
	"sort"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// OrderParts is items in play order (2026-11-10 design D11): by
// spec.partIndex, which the import sets from the tags' track number, else
// the name's part number, else the path's sort order (fileimport.PartIndexOf);
// a file with no part index (0) comes after every numbered part; ties, and
// the unnumbered, order by spec.path and then by name, the order FileState
// kept before the index existed. items is left as it is.
func OrderParts(items []catalogv1alpha1.MediaFile) []catalogv1alpha1.MediaFile {
	sorted := make([]catalogv1alpha1.MediaFile, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i].Spec.PartIndex, sorted[j].Spec.PartIndex
		switch {
		case a > 0 && b > 0 && a != b:
			return a < b
		case (a > 0) != (b > 0):
			return a > 0
		}
		if sorted[i].Spec.Path != sorted[j].Spec.Path {
			return sorted[i].Spec.Path < sorted[j].Spec.Path
		}
		return sorted[i].Name < sorted[j].Name
	})
	return sorted
}

// PartRefs is status.fileRefs and status.partCount from the MediaFiles
// backing an audiobook: the names in play order (OrderParts), at most
// maxFileRefs of them (AudiobookStatus.FileRefs' MaxItems; a rejected apply
// would drop every other field of the one PatchStatus), and the count of
// parts on disk, every file counted whether or not its name fits the list.
func PartRefs(items []catalogv1alpha1.MediaFile) (refs []string, partCount int32) {
	if len(items) == 0 {
		return nil, 0
	}
	sorted := OrderParts(items)
	refs = make([]string, 0, len(sorted))
	for i := range sorted {
		refs = append(refs, sorted[i].Name)
	}
	if len(refs) > maxFileRefs {
		refs = refs[:maxFileRefs]
	}
	return refs, int32(len(sorted)) //nolint:gosec // bounded by the item's files, far under int32
}

// AuthorRefFromBook is the Author an Audiobook takes from the Book it is a
// recording of (2026-11-10 design D11: spec.authorRef is "set from the
// Book's author when bookRef resolves"): the Book's spec.authorRef, when
// the Audiobook names no author of its own and the Book names one. A
// standalone Book (no author) gives nothing, and an authorRef a person or
// Add New set is never replaced. book is the resolved spec.bookRef, nil
// when it does not resolve.
func AuthorRefFromBook(m *catalogv1alpha1.Audiobook, book *catalogv1alpha1.Book) (string, bool) {
	if m == nil || book == nil {
		return "", false
	}
	if m.Spec.AuthorRef != nil && *m.Spec.AuthorRef != "" {
		return "", false
	}
	if book.Spec.AuthorRef == nil || *book.Spec.AuthorRef == "" {
		return "", false
	}
	return *book.Spec.AuthorRef, true
}

// LinkAuthor writes spec.authorRef on m, once: a JSON merge patch of that
// one spec field, the shape of the Series' one-time classification patch
// (series/reconciler.go), under field owner k8s.ManagerCatalog -- the
// catalog controller manager, which is what writes it; a merge patch is no
// server-side apply, so it claims no ownership set and releases nothing.
// The spec change bumps the generation, which wakes the item once more; the
// next pass finds the ref set and AuthorRefFromBook answers nothing.
func LinkAuthor(ctx context.Context, cl client.Client, m *catalogv1alpha1.Audiobook, authorRef string) error {
	body, err := json.Marshal(map[string]any{"spec": map[string]any{"authorRef": authorRef}})
	if err != nil {
		return err
	}
	return cl.Patch(ctx, m.DeepCopy(), client.RawPatch(types.MergePatchType, body), client.FieldOwner(k8s.ManagerCatalog))
}
