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

// Package itemindex indexes MediaFiles by the catalog item they belong to
// (spec.mediaRef), on captionarr's manager cache.
//
// Both captionarr controllers map a Movie or Episode event onto that
// item's MediaFiles. Done as a namespace-wide List filtered in Go, every
// event deep-copied every MediaFile, and a cache's initial sync delivers
// every item as a create -- which the Kind source waits on before it counts
// as synced (controller-runtime's handlerRegistration.HasSynced). On the
// owner's library (16,450 items, 13,754 MediaFiles, two controllers) that
// was ~450 million copies on one core, the Episode source never synced
// inside the ten-minute CacheSyncTimeout, and captionarr crash-looped
// (2026-09-29). Through this index each event touches only its own files.
package itemindex

import (
	"context"
	"fmt"
	"sync"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// MediaFileByItem is the field index a MediaFile is found under by its
// item: [Key] of spec.mediaRef's kind and name.
const MediaFileByItem = "captionarr.spec.mediaRef"

// Key is the index value of the item kind/name.
func Key(kind commonv1.MediaKind, name string) string { return string(kind) + "/" + name }

// Extract is the [MediaFileByItem] indexer: a MediaFile's item, or nothing
// for a file attributed to none.
func Extract(o client.Object) []string {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok || mf.Spec.MediaRef.Kind == "" || mf.Spec.MediaRef.Name == "" {
		return nil
	}
	return []string{Key(mf.Spec.MediaRef.Kind, mf.Spec.MediaRef.Name)}
}

// MediaFilesOf lists the MediaFiles of the item kind/name in namespace
// through [MediaFileByItem].
func MediaFilesOf(
	ctx context.Context, c client.Reader, namespace string, kind commonv1.MediaKind, name string,
) ([]catalogv1alpha1.MediaFile, error) {
	var files catalogv1alpha1.MediaFileList
	if err := c.List(ctx, &files, client.InNamespace(namespace),
		client.MatchingFields{MediaFileByItem: Key(kind, name)}); err != nil {
		return nil, fmt.Errorf("itemindex: list MediaFiles of %s %s/%s: %w", kind, namespace, name, err)
	}
	return files.Items, nil
}

var (
	mu         sync.Mutex
	registered = map[client.FieldIndexer]bool{}
)

// Register adds [MediaFileByItem] to mgr's cache once. Both controllers
// call it from SetupWithManager: a second IndexField for one (type, field)
// on one cache is an "indexer conflict" at start, so a repeat is a no-op.
func Register(ctx context.Context, mgr ctrl.Manager) error {
	mu.Lock()
	defer mu.Unlock()
	idx := mgr.GetFieldIndexer()
	if registered[idx] {
		return nil
	}
	if err := idx.IndexField(ctx, &catalogv1alpha1.MediaFile{}, MediaFileByItem, Extract); err != nil {
		return fmt.Errorf("itemindex: index MediaFile by item: %w", err)
	}
	registered[idx] = true
	return nil
}
