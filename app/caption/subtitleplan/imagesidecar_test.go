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

package subtitleplan

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/providerset"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/subtitlestore"
)

// A .sup beside the file -- the PGS track the MP4 standard kept for the
// OCR (standard.Version 8) -- is no subtitle a player reads: the
// language still wants a text one, so it is not an existing subtitle,
// where the .srt beside it is.
func TestAnImageSidecarIsNoExistingSubtitle(t *testing.T) {
	got := sidecarExisting([]string{"Movie (1999).en.sup", "Movie (1999).fr.srt", "Movie (1999).mp4"}, "Movie (1999).mp4")
	var keys []string
	for _, e := range got {
		keys = append(keys, e.langKey)
	}
	assert.Equal(t, []string{"fr"}, keys, "%+v", got)
}

// englishAudioFile is the owner's case: an English-audio movie under the
// default english profile (audioExclude, so English subtitles are not
// wanted), with the directory listed as names says, an ocr provider when
// readers is true, and everything a fresh dispatch needs.
func englishAudioFile(t *testing.T, names []string, readers bool, originalLanguage string) Input {
	t.Helper()
	now := time.Date(2026, 10, 27, 18, 0, 0, 0, time.UTC)
	const path = "/data/media/tv/The Sopranos/Season 01/The Sopranos - S01E01 - Pilot.mp4"
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "sopranos-s01e01", UID: "uid-file"},
		Spec: catalogv1alpha1.MediaFileSpec{
			Path: path, MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "sopranos-s01e01"},
		},
	}
	mf.Status.ProbeHash = "probe-1"
	mf.Status.MediaInfo = &commonv1.MediaInfo{Audio: []commonv1.AudioStream{{Language: "eng"}}}
	profile := subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "english", Generation: 1},
		Spec: subtitlev1alpha1.SubtitleProfileSpec{
			Default:   true,
			Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en", AudioExclude: true, HI: subtitlev1alpha1.HIPolicyPrefer}},
		},
	}
	in := Input{
		File: mf, ProbeCurrent: true, Item: Item{Exists: true, OriginalLanguage: originalLanguage, Query: schema.SubtitleQuery{Kind: commonv1.MediaKindEpisode}},
		Profiles:    []subtitlev1alpha1.SubtitleProfile{profile},
		Listing:     &Listing{Names: append([]string{"The Sopranos - S01E01 - Pilot.mp4"}, names...)},
		BacklogOpen: true, PacerSlot: now, Now: now,
		Records: map[string]subtitlestore.Entry{"en": {}},
	}
	if readers {
		in.OCR = []providerset.Entry{{Name: "ocr", Type: subtitlev1alpha1.SubtitleProviderOCR}}
	}
	return in
}

// The owner, 2026-10-27: "convert .sup to .srt when .sup is the only
// subtitle format provided in the blu-ray". An English PGS sidecar beside
// an English-audio episode makes English wanted although the profile's
// audioExclude would not want it, and the fetch task asks the file's own
// subtitles alone: the sidecar is named, LocalOnly is set.
func TestAnImageSidecarMakesItsLanguageWantedFromTheFileAlone(t *testing.T) {
	in := englishAudioFile(t, []string{"The Sopranos - S01E01 - Pilot.en.sup"}, true, "en")
	d := Plan(in, func(int64) int64 { return 1 })
	require.NotNil(t, d.Block)
	assert.Equal(t, []string{"en"}, d.Block.Wanted)
	assert.False(t, d.Block.CutoffMet, "the disc's own subtitle is still to be read")
	require.Len(t, d.Dispatches, 1, "%+v", d.Block)
	task := d.Dispatches[0].Task
	assert.True(t, task.LocalOnly, "no remote provider is asked for a language the audio rules excluded")
	assert.Equal(t, []schema.ImageSidecar{{Name: "The Sopranos - S01E01 - Pilot.en.sup", LangKey: "en"}}, task.ImageSidecars)
	assert.Equal(t, catalogv1alpha1.SubtitlesPhaseSearching, d.Block.Phase)
}

// Without an ocr provider nothing can read the sidecar, so the audio rules
// stand and nothing is wanted; with the text subtitle beside it, the
// language exists and nothing is wanted either.
func TestAnImageSidecarWantsNothingWithoutAReaderOrBesideItsText(t *testing.T) {
	in := englishAudioFile(t, []string{"The Sopranos - S01E01 - Pilot.en.sup"}, false, "en")
	d := Plan(in, func(int64) int64 { return 1 })
	require.NotNil(t, d.Block)
	assert.Empty(t, d.Block.Wanted)
	assert.Empty(t, d.Dispatches)

	in = englishAudioFile(t, []string{"The Sopranos - S01E01 - Pilot.en.sup", "The Sopranos - S01E01 - Pilot.en.srt"}, true, "en")
	d = Plan(in, func(int64) int64 { return 1 })
	require.NotNil(t, d.Block)
	assert.Empty(t, d.Block.Wanted, "the .srt the OCR wrote satisfies the language")
	assert.Empty(t, d.Dispatches)
}

// A bare <stem>.sup -- an untagged track's -- is the item's original
// language, as an untagged audio track is: The Sopranos' is English and
// wanted; a Japanese film's is Japanese, which the profile does not want.
// It is listed in status.sidecars with no language, so a rename or a delete
// carries it.
func TestABareImageSidecarIsTheItemsOriginalLanguage(t *testing.T) {
	in := englishAudioFile(t, []string{"The Sopranos - S01E01 - Pilot.sup"}, true, "en")
	d := Plan(in, func(int64) int64 { return 1 })
	require.NotNil(t, d.Block)
	assert.Equal(t, []string{"en"}, d.Block.Wanted)
	require.Len(t, d.Dispatches, 1)
	assert.Equal(t, []schema.ImageSidecar{{Name: "The Sopranos - S01E01 - Pilot.sup", LangKey: "en"}}, d.Dispatches[0].Task.ImageSidecars)
	assert.Equal(t, []catalogv1alpha1.Sidecar{{Name: "The Sopranos - S01E01 - Pilot.sup"}}, d.Sidecars)

	in = englishAudioFile(t, []string{"The Sopranos - S01E01 - Pilot.sup"}, true, "ja")
	d = Plan(in, func(int64) int64 { return 1 })
	require.NotNil(t, d.Block)
	assert.Empty(t, d.Block.Wanted, "a Japanese image subtitle is not the English the profile wants")
	assert.Empty(t, d.Dispatches)
}

// A forced-only image sidecar does not make the plain language wanted: the
// fetch worker's filter would refuse the forced candidate, and the task
// would search for ever.
func TestAForcedImageSidecarDoesNotWantThePlainLanguage(t *testing.T) {
	in := englishAudioFile(t, []string{"The Sopranos - S01E01 - Pilot.en.forced.sup"}, true, "en")
	d := Plan(in, func(int64) int64 { return 1 })
	require.NotNil(t, d.Block)
	assert.Empty(t, d.Block.Wanted)
}

// A language the profile wants anyway (a Japanese-audio film's English)
// is a normal want: every provider is asked, the ocr one first as the local
// tier, so the task carries the sidecar without LocalOnly.
func TestAWantedLanguagesImageSidecarRidesANormalTask(t *testing.T) {
	in := englishAudioFile(t, []string{"The Sopranos - S01E01 - Pilot.en.sup"}, true, "ja")
	in.File.Status.MediaInfo.Audio = []commonv1.AudioStream{{Language: "jpn"}}
	d := Plan(in, func(int64) int64 { return 1 })
	require.NotNil(t, d.Block)
	assert.Equal(t, []string{"en"}, d.Block.Wanted)
	require.Len(t, d.Dispatches, 1)
	task := d.Dispatches[0].Task
	assert.False(t, task.LocalOnly)
	assert.Equal(t, []schema.ImageSidecar{{Name: "The Sopranos - S01E01 - Pilot.en.sup", LangKey: "en"}}, task.ImageSidecars)
}
