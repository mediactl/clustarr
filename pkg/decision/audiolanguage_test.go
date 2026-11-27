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

package decision

import (
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

func TestLacksLanguage(t *testing.T) {
	orig := quality.Profile{Language: "original"}
	cases := []struct {
		name  string
		p     quality.Profile
		orig  string
		audio []string
		want  bool
	}{
		{"korean-only file of a japanese show", orig, "ja", []string{"ko"}, true},
		{"dual audio carries the original", orig, "ja", []string{"en", "ja"}, false},
		{"region subtags compare by base", quality.Profile{Language: "pt"}, "", []string{"pt-BR"}, false},
		{"unknown audio is not wrong", orig, "ja", nil, false},
		{"unknown original constrains nothing", orig, "", []string{"ko"}, false},
		{"any wants nothing", quality.Profile{Language: "any"}, "ja", []string{"ko"}, false},
		{"empty wants nothing", quality.Profile{}, "ja", []string{"ko"}, false},
		{"an explicit tag", quality.Profile{Language: "en"}, "ja", []string{"ja"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { require.Equal(t, c.want, LacksLanguage(c.p, c.orig, c.audio)) })
	}
}

// A file's one audio track with no tag is the one language its record names
// (owner, 2026-11-27: "Treat a file's single untagged track as the language
// its record already names. The untagged language default is only suitable
// (and only partially) for dual language where the original lang is already
// tagged"); every other untagged shape stays unknown.
func TestFileAudioLanguagesNameASingleUntaggedTrack(t *testing.T) {
	tracks := func(langs ...string) *common.MediaInfo {
		mi := &common.MediaInfo{}
		for _, l := range langs {
			mi.Audio = append(mi.Audio, common.AudioStream{Language: l})
		}
		return mi
	}
	for _, tc := range []struct {
		name     string
		mi       *common.MediaInfo
		recorded []string
		want     []string
	}{
		{"one untagged track, one recorded language", tracks("und"), []string{"Japanese"}, []string{"ja"}},
		{"an empty tag is no tag", tracks(""), []string{"Japanese"}, []string{"ja"}},
		{"a muxer's unk is no tag", tracks("unk"), []string{"English"}, []string{"en"}},
		{"tagged tracks are read as they are", tracks("eng", "jpn"), []string{"Japanese"}, []string{"en", "ja"}},
		{"a tagged track outranks the record", tracks("jpn"), []string{"English"}, []string{"ja"}},
		{"the record names several languages", tracks("und"), []string{"English", "Japanese"}, nil},
		{"the record names none", tracks("und"), nil, nil},
		{"a name the catalogue does not carry", tracks("und"), []string{"Klingon"}, nil},
		{"the original tagged beside an untagged track: the dub may be it", tracks("jpn", "und"), []string{"Japanese"}, nil},
		{"two untagged tracks", tracks("und", "und"), []string{"Japanese"}, nil},
		{"no probe", nil, []string{"Japanese"}, nil},
		{"no audio", tracks(), []string{"Japanese"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, FileAudioLanguages(tc.mi, tc.recorded))
		})
	}
}
