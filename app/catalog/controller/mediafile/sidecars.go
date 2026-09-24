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

package mediafile

import (
	"fmt"
	"path/filepath"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// sidecarsFromSubtitleRequest is §8.6's "sidecar feedback path": it mirrors
// a SubtitleRequest's downloaded/upgradable items onto MediaFile.status.
// sidecars. It never walks the filesystem itself -- pkg/subtitles' own doc
// comment says sidecar-path naming belongs to captionarr (Phase F); this
// function only trusts what captionarr already wrote to status.items.path,
// which spec §4.6 documents as relative to the media file's directory,
// where MediaFileStatus.Sidecars.Path is documented absolute.
func sidecarsFromSubtitleRequest(mediaFileDir string, items []subtitlev1alpha1.SubtitleItem) ([]catalogv1alpha1.Sidecar, error) {
	out := make([]catalogv1alpha1.Sidecar, 0, len(items))
	for _, it := range items {
		if it.Path == "" {
			continue
		}
		if it.State != subtitlev1alpha1.SubtitleItemDownloaded && it.State != subtitlev1alpha1.SubtitleItemUpgradable {
			continue
		}
		lang, forced, hi, err := subtitles.ParseLangKey(subtitles.LangKey(it.LangKey))
		if err != nil {
			return nil, fmt.Errorf("mediafile: sidecar langKey %q: %w", it.LangKey, err)
		}
		out = append(out, catalogv1alpha1.Sidecar{
			Path:     filepath.Join(mediaFileDir, it.Path),
			Language: lang,
			Forced:   forced,
			HI:       hi,
		})
	}
	return out, nil
}
