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

package rollup_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/pkg/quality"
)

func TestProbedAudioLanguages(t *testing.T) {
	mf := func(langs ...string) *catalogv1alpha1.MediaFile {
		var a []commonv1.AudioStream
		for _, l := range langs {
			a = append(a, commonv1.AudioStream{Language: l})
		}
		return &catalogv1alpha1.MediaFile{Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{Audio: a}}}
	}
	require.Equal(t, []string{"en", "ja"}, rollup.ProbedAudioLanguages(mf("eng", "jpn")))
	require.Equal(t, []string{"ja"}, rollup.ProbedAudioLanguages(mf("jpn", "jpn")), "deduplicated")
	require.Equal(t, []string{"ko"}, rollup.ProbedAudioLanguages(mf("kor")))
	require.Nil(t, rollup.ProbedAudioLanguages(mf("jpn", "und")), "one untagged track makes the set unknown")
	require.Nil(t, rollup.ProbedAudioLanguages(mf("jpn", "")), "an empty tag too")
	require.Nil(t, rollup.ProbedAudioLanguages(mf("unk")), `"unk" is an "unknown" placeholder, not a language (75 Mister Rogers files, 2026-10-06)`)
	require.Nil(t, rollup.ProbedAudioLanguages(mf()), "no audio streams: unknown")
	require.Nil(t, rollup.ProbedAudioLanguages(&catalogv1alpha1.MediaFile{}), "not probed")
	require.Nil(t, rollup.ProbedAudioLanguages(nil))
}

func TestAudioLanguagesObjectKeysTheProbedLanguages(t *testing.T) {
	mf := &catalogv1alpha1.MediaFile{Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{
		Audio: []commonv1.AudioStream{{Language: "eng"}, {Language: "jpn"}}}}}
	require.Equal(t, "en,ja", rollup.AudioLanguagesObject(mf))
	require.Equal(t, "", rollup.AudioLanguagesObject(&catalogv1alpha1.MediaFile{}))
	require.Equal(t, "", rollup.AudioLanguagesObject(&catalogv1alpha1.Episode{}), "not a MediaFile")
}

func TestAudioStateFor(t *testing.T) {
	p := &quality.Profile{AudioLanguages: []string{"en", "original"}, AudioGraft: true}
	mf := func(langs ...string) *catalogv1alpha1.MediaFile {
		var a []commonv1.AudioStream
		for _, l := range langs {
			a = append(a, commonv1.AudioStream{Language: l})
		}
		return &catalogv1alpha1.MediaFile{Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{Audio: a}}}
	}
	require.Equal(t, &catalogv1alpha1.AudioState{Wanted: []string{"en", "ja"}, Present: []string{"ja"}, Missing: []string{"en"}, Graft: "none"},
		rollup.AudioStateFor(p, "ja", mf("jpn")))
	require.Equal(t, &catalogv1alpha1.AudioState{Wanted: []string{"en", "ja"}, Present: []string{"en", "ja"}, Graft: "none"},
		rollup.AudioStateFor(p, "ja", mf("eng", "jpn")))
	require.Equal(t, &catalogv1alpha1.AudioState{Wanted: []string{"en", "ja"}, Graft: "none"},
		rollup.AudioStateFor(p, "ja", mf("und")), "unknown audio: nothing is called missing")
	require.Equal(t, []string{"en"}, rollup.AudioStateFor(p, "", mf("jpn")).Wanted, "an unknown original is dropped")
	require.Equal(t, "", rollup.AudioStateFor(&quality.Profile{AudioLanguages: []string{"en"}}, "ja", mf("eng")).Graft, "no graft, no graft state")
	require.Nil(t, rollup.AudioStateFor(&quality.Profile{}, "ja", mf("jpn")), "no audio policy")
	require.Nil(t, rollup.AudioStateFor(nil, "ja", mf("jpn")))
	require.Nil(t, rollup.AudioStateFor(p, "ja", nil), "no file")
}
