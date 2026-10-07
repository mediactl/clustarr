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
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
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

// FieldIndexes declares the one index the file-import worker reads:
// [MediaFileByTargetIndexKey] on MediaFile. The import domain declares it
// and the process registers it once (spec §3.5.2 step 9, §5.7).
func FieldIndexes() []k8s.FieldIndex {
	return []k8s.FieldIndex{{
		Object: &catalogv1alpha1.MediaFile{}, Name: MediaFileByTargetIndexKey, Extract: mediaFileTargetKeys,
	}}
}

// mediaFileTargetKeys is [MediaFileByTargetIndexKey]'s value function.
func mediaFileTargetKeys(o client.Object) []string {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok || mf.Spec.MediaRef.Name == "" {
		return nil
	}
	ref := mf.Spec.MediaRef
	keys := []string{targetKey(string(ref.Kind), ref.Name)}
	if ref.Kind == commonv1.MediaKindEpisode {
		// A multi-episode file backs every episode in keys
		// (EpisodeFileRef), and is each one's existing file.
		for _, k := range ref.Keys {
			if k != ref.Name {
				keys = append(keys, targetKey(string(ref.Kind), k))
			}
		}
	}
	return keys
}

// targetKey builds the index value for a MediaRef's kind and name.
func targetKey(kind, name string) string { return kind + "/" + name }
