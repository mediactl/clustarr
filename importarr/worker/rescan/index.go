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

package rescan

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// MediaFilePathIndexKey is the cache field index the rescan worker looks a
// walked file up by. A library rescan asks "is there already a MediaFile for
// this exact path?" once per file, and listing every MediaFile in the
// namespace per file would make a large library quadratic.
const MediaFilePathIndexKey = "spec.path"

// IndexMediaFileByPath registers [MediaFilePathIndexKey] on the manager's
// cache. It must be called before the manager starts -- Task C12 calls it
// from importarr's setupWorkers -- because the informer is built with the
// indexes it was given.
func IndexMediaFileByPath(ctx context.Context, mgr ctrl.Manager) error {
	return mgr.GetFieldIndexer().IndexField(ctx, &catalogv1alpha1.MediaFile{}, MediaFilePathIndexKey,
		func(o client.Object) []string {
			mf, ok := o.(*catalogv1alpha1.MediaFile)
			if !ok || mf.Spec.Path == "" {
				return nil
			}
			return []string{mf.Spec.Path}
		})
}
