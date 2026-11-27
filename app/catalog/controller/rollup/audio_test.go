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
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/dubs"
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
		Audio: []commonv1.AudioStream{{Language: "eng"}, {Language: "jpn"}},
	}}}
	require.Equal(t, "en,ja", rollup.AudioLanguagesObject(mf))
	require.Equal(t, "", rollup.AudioLanguagesObject(&catalogv1alpha1.MediaFile{}))
	require.Equal(t, "", rollup.AudioLanguagesObject(&catalogv1alpha1.Episode{}), "not a MediaFile")
}

func TestAudioStateFor(t *testing.T) {
	p := &quality.Profile{AudioLanguages: []string{"en", "original"}, GraftAudio: true}
	mf := func(langs ...string) *catalogv1alpha1.MediaFile {
		var a []commonv1.AudioStream
		for _, l := range langs {
			a = append(a, commonv1.AudioStream{Language: l})
		}
		return &catalogv1alpha1.MediaFile{Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{Audio: a}}}
	}
	require.Equal(t, &catalogv1alpha1.AudioState{Wanted: []string{"en", "ja"}, Present: []string{"ja"}, Missing: []string{"en"}, Graft: "searching"},
		rollup.AudioStateFor(p, "ja", mf("jpn"), rollup.GraftObservation{}))
	require.Equal(t, &catalogv1alpha1.AudioState{Wanted: []string{"en", "ja"}, Present: []string{"en", "ja"}, Graft: "none"},
		rollup.AudioStateFor(p, "ja", mf("eng", "jpn"), rollup.GraftObservation{}))
	require.Equal(t, &catalogv1alpha1.AudioState{Wanted: []string{"en", "ja"}, Graft: "none"},
		rollup.AudioStateFor(p, "ja", mf("und"), rollup.GraftObservation{}), "unknown audio: nothing is called missing")
	require.Equal(t, []string{"en"}, rollup.AudioStateFor(p, "", mf("jpn"), rollup.GraftObservation{}).Wanted, "an unknown original is dropped")
	require.Equal(t, []string{"en"}, rollup.AudioStateFor(p, "en", mf("eng"), rollup.GraftObservation{}).Wanted,
		"an English original is wanted once, not [en, en] (phase 2 review)")
	require.Equal(t, "", rollup.AudioStateFor(&quality.Profile{AudioLanguages: []string{"en"}}, "ja", mf("eng"), rollup.GraftObservation{}).Graft, "no graft, no graft state")
	require.Nil(t, rollup.AudioStateFor(&quality.Profile{}, "ja", mf("jpn"), rollup.GraftObservation{}), "no audio policy")
	require.Nil(t, rollup.AudioStateFor(nil, "ja", mf("jpn"), rollup.GraftObservation{}))
	require.Nil(t, rollup.AudioStateFor(p, "ja", nil, rollup.GraftObservation{}), "no file")

	// An anime whose dub is still airing (decision.ForDub, Incomplete): its
	// optional English is missing like any other language, so the donor
	// search keeps looking; one the MAL-Dubs list does not name wants none.
	incomplete := decision.ForDub(*p, dubs.Incomplete)
	require.Equal(t, &catalogv1alpha1.AudioState{Wanted: []string{"ja", "en"}, Present: []string{"ja"}, Missing: []string{"en"}, Graft: "searching"},
		rollup.AudioStateFor(&incomplete, "ja", mf("jpn"), rollup.GraftObservation{}))
	none := decision.ForDub(*p, dubs.NotDubbed)
	require.Equal(t, &catalogv1alpha1.AudioState{Wanted: []string{"ja"}, Present: []string{"ja"}, Graft: "none"},
		rollup.AudioStateFor(&none, "ja", mf("jpn"), rollup.GraftObservation{}))
}

// The anchor a graft aligns on is the item's original language, and for a
// file taken as a dub alone the dub it carries (owner, 2026-11-27: "Allow
// dubs-only (or vice versa - *with* the graft)"): its missing language is
// the original, which no track of it could be aligned against. A file whose
// audio is unknown, or that carries the original, anchors on the original
// as before; one carrying no wanted language has no anchor, so no donor is
// searched for it.
func TestTheGraftAnchorOfADubAloneIsItsDub(t *testing.T) {
	p := &quality.Profile{AudioLanguages: []string{"en", "original"}, GraftAudio: true, AudioCompleteFirst: true}
	state := func(langs ...string) *catalogv1alpha1.AudioState {
		var a []commonv1.AudioStream
		for _, l := range langs {
			a = append(a, commonv1.AudioStream{Language: l})
		}
		mf := &catalogv1alpha1.MediaFile{Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{Audio: a}}}
		return rollup.AudioStateFor(p, "ja", mf, rollup.GraftObservation{})
	}

	dub := state("eng")
	require.Equal(t, []string{"ja"}, dub.Missing)
	require.Equal(t, "searching", dub.Graft)
	require.Equal(t, "en", rollup.GraftAnchor("ja", dub), "the dub it carries")

	original := state("jpn")
	require.Equal(t, []string{"en"}, original.Missing)
	require.Equal(t, "ja", rollup.GraftAnchor("ja", original))
	require.Equal(t, "ja", rollup.GraftAnchor("ja", state("jpn", "und")), "unknown audio is not known to lack it")
	require.Equal(t, "ja", rollup.GraftAnchor("ja", state("eng", "jpn")))
	require.Equal(t, "ja", rollup.GraftAnchor("ja", nil))

	require.Equal(t, "", rollup.GraftAnchor("ja", state("ger")), "nothing to align on")
	require.Equal(t, "", rollup.GraftAnchor("", dub), "an unknown original stays unknown")
}

// A graft is open -- a torrent pack waits on it (grabplan's grafts) -- while
// the file lacks a language and a donor is on its way or the donor search
// has not run out (owner, 2026-11-27: "If no eng release exists whatsoever
// that we can download (unlikely) - we can then finally fallback to a full
// season re-download from torrent for dual").
func TestAGraftIsOpenUntilItsDonorSearchesRunOut(t *testing.T) {
	searched := func(n int32) commonv1.Attempts { return commonv1.Attempts{Count: n} }
	missing := func(graft, reason string) *catalogv1alpha1.AudioState {
		return &catalogv1alpha1.AudioState{Wanted: []string{"en", "ja"}, Present: []string{"ja"}, Missing: []string{"en"}, Graft: graft, Reason: reason}
	}
	for _, tc := range []struct {
		name     string
		audio    *catalogv1alpha1.AudioState
		searches int32
		want     bool
	}{
		{"no audio state", nil, 0, false},
		{"nothing missing", &catalogv1alpha1.AudioState{Wanted: []string{"en", "ja"}, Present: []string{"en", "ja"}, Graft: "none"}, 0, false},
		{"searching, never searched", missing("searching", ""), 0, true},
		{"searching, searches left", missing("searching", ""), rollup.GraftDonorSearches - 1, true},
		{"searching, searches run out", missing("searching", ""), rollup.GraftDonorSearches, false},
		{"a donor downloading, whatever the count", missing("grabbed", ""), 9, true},
		{"the graft waiting or running", missing("pending", ""), 9, true},
		{"aligned", missing("aligned", ""), 9, true},
		{"a failed graft, another donor to find", missing("failed", "DonorFault"), 1, true},
		{"a failed graft, searches run out", missing("failed", "DonorFault"), rollup.GraftDonorSearches, false},
		{"a file that backs several items is never grafted", missing("failed", rollup.ReasonMultiItemFile), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, rollup.GraftOpen(tc.audio, searched(tc.searches)))
		})
	}
}

// A file's one untagged track is the language its record names, so an item
// whose file is a subbed Japanese release with no audio tag lacks English
// and is grafted, where it read as unknown and final (talos, 2026-11-27:
// 139 One Piece episodes; decision.FileAudioLanguages).
func TestASingleUntaggedTrackIsTheRecordedLanguage(t *testing.T) {
	p := &quality.Profile{AudioLanguages: []string{"en", "original"}, GraftAudio: true, AudioCompleteFirst: true}
	file := func(recorded []string, langs ...string) *catalogv1alpha1.MediaFile {
		var a []commonv1.AudioStream
		for _, l := range langs {
			a = append(a, commonv1.AudioStream{Language: l})
		}
		return &catalogv1alpha1.MediaFile{
			Spec:   catalogv1alpha1.MediaFileSpec{Languages: recorded},
			Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{Audio: a}},
		}
	}

	subbed := file([]string{"Japanese"}, "und")
	require.Equal(t, []string{"ja"}, rollup.ProbedAudioLanguages(subbed))
	st := rollup.AudioStateFor(p, "ja", subbed, rollup.GraftObservation{})
	require.Equal(t, []string{"ja"}, st.Present)
	require.Equal(t, []string{"en"}, st.Missing)
	require.Equal(t, "searching", st.Graft)
	require.Equal(t, "ja", rollup.GraftAnchor("ja", st))

	// The original tagged beside an untagged track stays unknown: the
	// untagged one may be the dub.
	require.Nil(t, rollup.ProbedAudioLanguages(file([]string{"Japanese"}, "jpn", "und")))
	require.Empty(t, rollup.AudioStateFor(p, "ja", file([]string{"Japanese"}, "jpn", "und"), rollup.GraftObservation{}).Missing)
	// A record naming two languages names neither for one track.
	require.Nil(t, rollup.ProbedAudioLanguages(file([]string{"English", "Japanese"}, "und")))
}
