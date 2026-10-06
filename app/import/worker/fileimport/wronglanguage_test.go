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
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

func TestReplacesWrongLanguage(t *testing.T) {
	p := quality.Profile{Language: "original"}
	probe := func(langs ...string) *commonv1.MediaInfo {
		mi := &commonv1.MediaInfo{}
		for _, l := range langs {
			mi.Audio = append(mi.Audio, commonv1.AudioStream{Language: l})
		}
		return mi
	}
	file := func(langs ...string) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{Status: catalogv1alpha1.MediaFileStatus{MediaInfo: probe(langs...)}}
	}
	require.True(t, replacesWrongLanguage(p, "ja", file("kor"), probe("jpn")), "a Japanese file replaces a Korean-only one")
	require.True(t, replacesWrongLanguage(p, "ja", file("kor"), probe("eng", "jpn")), "dual audio carries it")
	require.False(t, replacesWrongLanguage(p, "ja", file("kor"), probe("kor")), "the new file is wrong too")
	require.False(t, replacesWrongLanguage(p, "ja", file("kor"), probe("jpn", "und")), "the new file's audio is not all known")
	require.False(t, replacesWrongLanguage(p, "ja", file("kor"), nil), "no probe of the new file")
	require.False(t, replacesWrongLanguage(p, "ja", file("jpn"), probe("jpn")), "the existing file is right: the usual upgrade rule applies")
	require.False(t, replacesWrongLanguage(p, "ja", file("und"), probe("jpn")), "the existing file's audio is unknown")
	require.False(t, replacesWrongLanguage(quality.Profile{Language: "any"}, "ja", file("kor"), probe("jpn")), "the profile wants no language")
}
