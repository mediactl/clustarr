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

package release_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

func TestClassifyKind(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  commonv1.MediaKind
	}{
		{"movie", "The.Matrix.1999.1080p.BluRay.x264-GROUP", commonv1.MediaKindMovie},
		{"episode", "Severance.S02E03.1080p.ATVP.WEB-DL.DDP5.1.Atmos.H.264-NTb", commonv1.MediaKindEpisode},
		{"season pack", "Fringe.S05.1080p.BluRay.x264-DEMAND", commonv1.MediaKindEpisode},
		{"anime bracket", "[SubsPlease] Frieren - 28 (1080p) [F02B9CDC].mkv", commonv1.MediaKindEpisode},
		{"music", "Pink Floyd - The Dark Side of the Moon (1973) [FLAC]", commonv1.MediaKindAlbum},
		{"ebook", "Andy Weir - Project Hail Mary (2021) [EPUB]", commonv1.MediaKindBook},
		{"audiobook", "Andy Weir - Project Hail Mary (Unabridged) [M4B 64kbps]", commonv1.MediaKindAudiobook},
		{"comic", "Saga 001 (2012) (Digital) (Zone-Empire).cbz", commonv1.MediaKindComic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, release.ClassifyKind(tt.title))
		})
	}
}
