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
package album_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/album"
	"github.com/mediactl/clustarr/pkg/quality"
)

// ladderProfile is a cut-down music-lossless ladder (best first), cutoff
// Lossless, built by hand the way rollup's own filestate tests build theirs.
func ladderProfile() *quality.Profile {
	return &quality.Profile{
		Tiers: [][]quality.Definition{
			{{Quality: commonv1.Quality{Name: "Lossless"}}, {Quality: commonv1.Quality{Name: "ALAC"}}}, // one tier, tied
			{{Quality: commonv1.Quality{Name: "High"}}},
			{{Quality: commonv1.Quality{Name: "Low"}}},
		},
		CutoffIndex: 0,
	}
}

// trackFile is one album MediaFile of quality q, created at minute min.
func trackFile(name, q string, minute int, formatScore int32) catalogv1alpha1.MediaFile {
	return catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC)),
		},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef:    commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "ok-computer"},
			Quality:     commonv1.Quality{Name: q},
			FormatScore: formatScore,
			Original:    ptr.To(true),
		},
	}
}

// TestFileStateTakesTheLowestTrack pins Lidarr's CutoffSpecification: the
// album is ranked by every file backing it, so one lower-quality track keeps
// it below the cutoff and is the quality it reports, whichever file is
// newest.
func TestFileStateTakesTheLowestTrack(t *testing.T) {
	transcoded := trackFile("t2-transcoded", "Low", 5, 0)
	transcoded.Spec.Original = ptr.To(false)

	cases := []struct {
		name        string
		files       []catalogv1alpha1.MediaFile
		profile     *quality.Profile
		wantHasFile bool
		wantQuality string
		wantScore   int32
		wantCutoff  bool
	}{
		{name: "no files is the zero value", profile: ladderProfile()},
		{
			name:    "every track at the cutoff meets it",
			files:   []catalogv1alpha1.MediaFile{trackFile("t1", "Lossless", 1, 0), trackFile("t2", "Lossless", 2, 0)},
			profile: ladderProfile(), wantHasFile: true, wantQuality: "Lossless", wantCutoff: true,
		},
		{
			name: "one lossy track below the cutoff keeps the album unmet and is its quality, though the newest file is lossless",
			files: []catalogv1alpha1.MediaFile{
				trackFile("t1", "Lossless", 1, 0), trackFile("t2", "High", 2, 0), trackFile("t3", "Lossless", 9, 0),
			},
			profile: ladderProfile(), wantHasFile: true, wantQuality: "High",
		},
		{
			name:    "the lowest of several tiers wins, in any list order",
			files:   []catalogv1alpha1.MediaFile{trackFile("t1", "High", 1, 0), trackFile("t2", "Low", 2, 0), trackFile("t3", "Lossless", 3, 0)},
			profile: ladderProfile(), wantHasFile: true, wantQuality: "Low",
		},
		{
			name:    "a quality the profile does not list ranks below every listed one",
			files:   []catalogv1alpha1.MediaFile{trackFile("t1", "Low", 1, 0), trackFile("t2", "MP3-8", 2, 0)},
			profile: ladderProfile(), wantHasFile: true, wantQuality: "MP3-8",
		},
		{
			name:    "a tie at the lowest tier is broken as rollup.PickMediaFile breaks it: newest",
			files:   []catalogv1alpha1.MediaFile{trackFile("t1", "ALAC", 7, 0), trackFile("t2", "Lossless", 3, 0)},
			profile: ladderProfile(), wantHasFile: true, wantQuality: "ALAC", wantCutoff: true,
		},
		{
			name:    "a transcoded file meets the cutoff whatever its frozen quality, as rollup.FileState's rule",
			files:   []catalogv1alpha1.MediaFile{trackFile("t1", "Lossless", 1, 0), transcoded},
			profile: ladderProfile(), wantHasFile: true, wantQuality: "Low", wantCutoff: true,
		},
		{
			name:        "no profile: cutoff unmet, quality from the file rollup.PickMediaFile selects",
			files:       []catalogv1alpha1.MediaFile{trackFile("t1", "Low", 1, 0), trackFile("t2", "Lossless", 2, 0)},
			wantHasFile: true, wantQuality: "Lossless",
		},
		{
			name:    "format score is the lowest file's",
			files:   []catalogv1alpha1.MediaFile{trackFile("t1", "Lossless", 1, 40), trackFile("t2", "Lossless", 2, -10), trackFile("t3", "Lossless", 3, 5)},
			profile: ladderProfile(), wantHasFile: true, wantQuality: "Lossless", wantScore: -10, wantCutoff: true,
		},
		{
			name: "a file of another kind is not the album's",
			files: func() []catalogv1alpha1.MediaFile {
				other := trackFile("m1", "Low", 1, 0)
				other.Spec.MediaRef = commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "ok-computer"}
				return []catalogv1alpha1.MediaFile{other, trackFile("t1", "Lossless", 2, 0)}
			}(),
			profile: ladderProfile(), wantHasFile: true, wantQuality: "Lossless", wantCutoff: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, order := range [][]catalogv1alpha1.MediaFile{tc.files, reversed(tc.files)} {
				hasFile, q, score, cutoff := album.FileState(order, tc.profile)
				assert.Equal(t, tc.wantHasFile, hasFile, "hasFile")
				if tc.wantQuality == "" {
					assert.Nil(t, q, "quality")
				} else {
					require.NotNil(t, q, "quality")
					assert.Equal(t, tc.wantQuality, q.Name, "quality")
				}
				assert.Equal(t, tc.wantScore, score, "formatScore")
				assert.Equal(t, tc.wantCutoff, cutoff, "cutoffMet")
			}
		})
	}
}

func reversed(in []catalogv1alpha1.MediaFile) []catalogv1alpha1.MediaFile {
	out := make([]catalogv1alpha1.MediaFile, len(in))
	for i := range in {
		out[len(in)-1-i] = in[i]
	}
	return out
}
