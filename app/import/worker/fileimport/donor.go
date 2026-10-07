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
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/fsops"
)

// DonorDir is the folder under a RootFolder that holds audio donors, one
// folder per item UID (anime dual-audio spec §6.2). Plex's scanner skips a
// dot folder, and so does every walk here (fsops.Classifier.Walk).
const DonorDir = fsops.ClustarrDir + "/donors"

// donorExts are the files a donor download can hold its audio in.
var donorExts = map[string]bool{".mkv": true, ".mka": true, ".mp4": true, ".m4v": true, ".avi": true, ".ts": true}

// donorItem is what a donor import needs of its Episode or Movie.
type donorItem struct {
	obj        client.Object
	audio      *catalogv1alpha1.AudioState
	original   string
	rootFolder string
	profileRef string
}

// donorItem reads the Episode (and its Series) or Movie a donor is for; nil
// when it is gone or of another kind.
func (w *Worker) donorItem(ctx context.Context, ns string, ref commonv1.MediaRef) (*donorItem, error) {
	key := types.NamespacedName{Namespace: ns, Name: ref.Name}
	switch ref.Kind {
	case commonv1.MediaKindEpisode:
		var e catalogv1alpha1.Episode
		if err := w.Client.Get(ctx, key, &e); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		var s catalogv1alpha1.Series
		if err := w.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: e.Spec.SeriesRef}, &s); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		it := &donorItem{obj: &e, audio: e.Status.Audio, rootFolder: s.Spec.RootFolderRef, profileRef: s.Spec.QualityProfileRef}
		if s.Status.Metadata != nil {
			it.original = s.Status.Metadata.OriginalLanguage
		}
		return it, nil
	case commonv1.MediaKindMovie:
		var mv catalogv1alpha1.Movie
		if err := w.Client.Get(ctx, key, &mv); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		it := &donorItem{obj: &mv, audio: mv.Status.Audio, rootFolder: mv.Spec.RootFolderRef, profileRef: mv.Spec.QualityProfileRef}
		if mv.Status.Metadata != nil {
			it.original = mv.Status.Metadata.OriginalLanguage
		}
		return it, nil
	}
	return nil, nil
}

// donorFile is the largest media or Matroska audio file under root that is
// not a sample: a donor release's one episode.
func donorFile(root string) (string, os.FileInfo, error) {
	var best string
	var bestInfo os.FileInfo
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !donorExts[strings.ToLower(filepath.Ext(p))] || strings.Contains(strings.ToLower(d.Name()), "sample") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if bestInfo == nil || info.Size() > bestInfo.Size() {
			best, bestInfo = p, info
		}
		return nil
	})
	return best, bestInfo, err
}

func kindOf(o client.Object) commonv1.MediaKind {
	if _, ok := o.(*catalogv1alpha1.Episode); ok {
		return commonv1.MediaKindEpisode
	}
	return commonv1.MediaKindMovie
}
