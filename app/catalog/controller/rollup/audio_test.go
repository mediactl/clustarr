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
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
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
}

// TestAudioStateForGraftStates: status.audio.graft is read from the item's
// donor Downloads and its AudioGraft (anime dual-audio spec §8: the item's
// reconciler is its one writer).
func TestAudioStateForGraftStates(t *testing.T) {
	p := &quality.Profile{AudioLanguages: []string{"en", "original"}, AudioGraft: true}
	mf := func(langs ...string) *catalogv1alpha1.MediaFile {
		var a []commonv1.AudioStream
		for _, l := range langs {
			a = append(a, commonv1.AudioStream{Language: l})
		}
		return &catalogv1alpha1.MediaFile{Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{Audio: a}}}
	}
	graft := func(phase transcodev1alpha1.AudioGraftPhase, reason string, segs int) *transcodev1alpha1.AudioGraft {
		g := &transcodev1alpha1.AudioGraft{Status: transcodev1alpha1.AudioGraftStatus{Phase: phase, Reason: reason, Message: "why"}}
		for range segs {
			g.Status.Segments = append(g.Status.Segments, transcodev1alpha1.AudioGraftSegment{LengthMillis: 1})
		}
		return g
	}
	cases := []struct {
		name   string
		file   *catalogv1alpha1.MediaFile
		g      rollup.GraftObservation
		want   string
		reason string
	}{
		{"missing, nothing under way", mf("jpn"), rollup.GraftObservation{}, "searching", ""},
		{"a donor downloads", mf("jpn"), rollup.GraftObservation{DonorDownloading: true}, "grabbed", ""},
		{"the graft waits", mf("jpn"), rollup.GraftObservation{Graft: graft(transcodev1alpha1.AudioGraftWaiting, "TranscodeRunning", 0)}, "pending", ""},
		{"the graft Job is made", mf("jpn"), rollup.GraftObservation{Graft: graft(transcodev1alpha1.AudioGraftPending, "", 0)}, "pending", ""},
		{"aligned and muxing", mf("jpn"), rollup.GraftObservation{Graft: graft(transcodev1alpha1.AudioGraftRunning, "", 1)}, "aligned", ""},
		{"it failed", mf("jpn"), rollup.GraftObservation{Graft: graft(transcodev1alpha1.AudioGraftFailed, "AlignmentRejected", 0)}, "failed", "AlignmentRejected: why"},
		{"a new donor beats a failed graft", mf("jpn"), rollup.GraftObservation{DonorDownloading: true, Graft: graft(transcodev1alpha1.AudioGraftFailed, "AlignmentRejected", 0)}, "grabbed", ""},
		{"swapped, awaiting the re-probe", mf("jpn"), rollup.GraftObservation{Graft: graft(transcodev1alpha1.AudioGraftSucceeded, "Grafted", 1)}, "pending", ""},
		{"grafted", mf("jpn", "eng"), rollup.GraftObservation{Graft: graft(transcodev1alpha1.AudioGraftSucceeded, "Grafted", 1)}, "done", ""},
		{"never needed one", mf("jpn", "eng"), rollup.GraftObservation{}, "none", ""},
		{"unknown audio", mf("und"), rollup.GraftObservation{Graft: graft(transcodev1alpha1.AudioGraftFailed, "x", 0)}, "none", ""},
	}
	for _, c := range cases {
		got := rollup.AudioStateFor(p, "ja", c.file, c.g)
		require.Equal(t, c.want, got.Graft, c.name)
		require.Equal(t, c.reason, got.Reason, c.name)
	}
}
