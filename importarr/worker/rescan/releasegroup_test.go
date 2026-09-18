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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// The corpus is real filenames run through pkg/release, so this test fails
// the day the parser is fixed and the guard becomes dead weight -- which is
// exactly when it should be deleted.
func TestReleaseGroupOrEmpty(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "Radarr movie-file layout leaks the resolution as the group",
			path: "/data/media/movies/Heat (1995) [tmdbid-949]/Heat (1995) [tmdbid-949] - Bluray-1080p.mkv",
			want: "",
		},
		{
			name: "dotted scene layout leaks the resolution in front of the real group",
			path: "/data/media/movies/Heat (1995)/Heat.1995.Bluray-1080p-RlsGrp.mkv",
			want: "",
		},
		{
			name: "a quality suffix with no group at all",
			path: "/data/media/movies/Heat (1995)/Heat (1995) WEBDL-720p.mkv",
			want: "",
		},
		{
			name: "an ordinary bluray scene release keeps its group",
			path: "/data/media/movies/Heat (1995)/Heat.1995.1080p.BluRay.x264-SPARKS.mkv",
			want: "SPARKS",
		},
		{
			name: "a 2160p release keeps its group",
			path: "/data/media/movies/Heat (1995)/Heat.1995.2160p.UHD.BluRay.x265-TERMiNAL.mkv",
			want: "TERMiNAL",
		},
		{
			name: "a web-dl release keeps its mixed-case group",
			path: "/data/media/movies/Heat (1995)/Heat.1995.1080p.WEB-DL.DDP5.1-NTb.mkv",
			want: "NTb",
		},
		{
			name: "a dvdrip keeps its group",
			path: "/data/media/movies/Heat (1995)/Heat.1995.DVDRip.XviD-FraMeSToR.mkv",
			want: "FraMeSToR",
		},
		{
			name: "no group parsed at all stays empty",
			path: "/data/media/movies/Heat (1995) [tmdbid-949]/Heat (1995) [tmdbid-949].mkv",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := release.ParsePath(tc.path, release.Options{Kind: commonv1.MediaKindMovie})
			require.NoError(t, err)
			assert.Equal(t, tc.want, releaseGroupOrEmpty(parsed))
		})
	}
}

// The guard works on the parsed group directly too, so its rule can be
// exercised without depending on what the parser happens to produce today.
func TestIsQualityToken(t *testing.T) {
	bluray1080p := commonv1.Quality{
		Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: 1080, Modifier: commonv1.ModifierNone,
	}
	remux := commonv1.Quality{
		Name: "Remux-2160p", Source: commonv1.SourceBluray, Resolution: 2160, Modifier: commonv1.ModifierRemux,
	}

	tests := []struct {
		name    string
		segment string
		quality commonv1.Quality
		want    bool
	}{
		{name: "a resolution token", segment: "1080p", quality: bluray1080p, want: true},
		{name: "an interlaced resolution token", segment: "1080i", quality: bluray1080p, want: true},
		{name: "a four-digit resolution token", segment: "2160p", quality: remux, want: true},
		{name: "a source word", segment: "BluRay", quality: bluray1080p, want: true},
		{name: "the parsed source itself", segment: "bluray", quality: bluray1080p, want: true},
		{name: "the parsed modifier", segment: "Remux", quality: remux, want: true},
		{name: "a source word the quality did not name", segment: "hdtv", quality: bluray1080p, want: true},

		{name: "an ordinary group", segment: "SPARKS", quality: bluray1080p},
		{name: "a group with digits", segment: "d3g", quality: bluray1080p},
		{name: "a group that merely contains a source word", segment: "WEBSTER", quality: bluray1080p},
		{name: "a group that merely contains a resolution", segment: "x1080pro", quality: bluray1080p},
		{name: "an empty segment", segment: "", quality: bluray1080p},
		{name: "a bare number is not a resolution", segment: "1080", quality: bluray1080p},
		// ModifierNone must not make the literal segment "none" a quality
		// token, or a group called None would vanish for the wrong reason.
		{name: "the none modifier is not a token", segment: "none", quality: bluray1080p},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isQualityToken(tc.segment, tc.quality))
		})
	}
}

// The guard never salvages: a contaminated group is dropped whole rather than
// half-parsed, because picking the real name out is the parser's job.
func TestReleaseGroupOrEmptyNeverSalvagesAPartialGroup(t *testing.T) {
	parsed := &release.ParsedRelease{
		Group:   "1080p-RlsGrp",
		Quality: commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: 1080},
	}
	assert.Empty(t, releaseGroupOrEmpty(parsed),
		"RlsGrp is probably the real group, but extracting it would be re-implementing pkg/release")
}
