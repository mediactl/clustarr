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

package fileimport

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// MediaFileByTargetIndexKey is the cache field index this worker looks a
// Download's target up by: "is there already a MediaFile for this catalog
// item?" once per import, not once per file, and listing every MediaFile in
// the namespace per import would make a large library quadratic.
//
// The value is "<kind>/<name>" rather than name alone so that a future kind
// (Episode, sharing the namespace with Movie) can never collide with a
// Movie of the same name.
const MediaFileByTargetIndexKey = ".spec.mediaRef.target"

// IndexMediaFileByTarget registers [MediaFileByTargetIndexKey] on the
// manager's cache. It must be called before the manager starts, exactly like
// importarr/worker/rescan.IndexMediaFileByPath -- the informer is built with
// the indexes it was given.
func IndexMediaFileByTarget(ctx context.Context, mgr ctrl.Manager) error {
	return mgr.GetFieldIndexer().IndexField(ctx, &catalogv1alpha1.MediaFile{}, MediaFileByTargetIndexKey,
		func(o client.Object) []string {
			mf, ok := o.(*catalogv1alpha1.MediaFile)
			if !ok || mf.Spec.MediaRef.Name == "" {
				return nil
			}
			return []string{targetKey(string(mf.Spec.MediaRef.Kind), mf.Spec.MediaRef.Name)}
		})
}

// targetKey builds the index value for a MediaRef's kind and name.
func targetKey(kind, name string) string { return kind + "/" + name }
