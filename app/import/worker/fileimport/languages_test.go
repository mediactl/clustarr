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

package fileimport_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/vansante/go-ffprobe.v2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// Solo Leveling's OldT pack (talos, 2026-10-27): its title says MULTI,
// which names no language, so each file froze as Japanese alone -- the
// series' original language -- while its probe carried fourteen audio
// tracks, and the library listed a one-language file named for fourteen.
// Sonarr's AggregateLanguage lets MediaInfo's audio tracks outrank the
// file's and the release's names; so does the import.
func TestAFilesLanguagesAreFrozenFromItsProbedAudio(t *testing.T) {
	name := "Historys.Greatest.Mysteries.S01E01.1080p.CR.WEB-DL.MULTI.AAC2.0.H.264-OldT.mkv"
	run, _ := packRun(t, "Historys.Greatest.Mysteries.S01.1080p.CR.WEB-DL.MULTI.AAC2.0.H.264-OldT", "ja", 1, []string{name},
		func(string) (*commonv1.MediaInfo, *mediainfo.Raw) {
			return &commonv1.MediaInfo{VideoCodec: "h264", Width: 1920, Height: 1080, Audio: []commonv1.AudioStream{
				{Codec: "aac", Language: "jpn"}, {Codec: "aac", Language: "deu"},
				{Codec: "aac", Language: "eng"}, {Codec: "aac", Language: "eng"},
			}}, &mediainfo.Raw{Format: &ffprobe.Format{TagList: ffprobe.Tags{}}}
		})
	run.decide()
	run.deliver()
	insp := run.record(schema.ImportSubInspect)
	require.NotNil(t, insp)
	require.NotNil(t, insp.Inspect)
	require.Len(t, insp.Inspect.Files, 1, "%+v", insp.Inspect)
	f := insp.Inspect.Files[0]
	assert.Empty(t, f.Rejections)
	assert.Equal(t, []string{"Japanese", "German", "English"}, f.Frozen.Languages,
		"the probe's tracks name the languages, in track order, each once")
}

// An untagged track leaves the probe no say: the names' reading stands,
// as pkg/decision's "unknown or untagged audio never lacks".
func TestAnUntaggedTrackLeavesTheNamesReadingOfTheLanguages(t *testing.T) {
	name := "Historys.Greatest.Mysteries.S01E01.1080p.CR.WEB-DL.MULTI.AAC2.0.H.264-OldT.mkv"
	run, _ := packRun(t, "Historys.Greatest.Mysteries.S01.1080p.CR.WEB-DL.MULTI.AAC2.0.H.264-OldT", "ja", 1, []string{name},
		func(string) (*commonv1.MediaInfo, *mediainfo.Raw) {
			return &commonv1.MediaInfo{VideoCodec: "h264", Width: 1920, Height: 1080, Audio: []commonv1.AudioStream{
				{Codec: "aac", Language: "jpn"}, {Codec: "aac"},
			}}, &mediainfo.Raw{Format: &ffprobe.Format{TagList: ffprobe.Tags{}}}
		})
	run.decide()
	run.deliver()
	insp := run.record(schema.ImportSubInspect)
	require.NotNil(t, insp)
	require.NotNil(t, insp.Inspect)
	require.Len(t, insp.Inspect.Files, 1, "%+v", insp.Inspect)
	f := insp.Inspect.Files[0]
	assert.Empty(t, f.Rejections)
	assert.Equal(t, []string{"Japanese"}, f.Frozen.Languages, "MULTI names nothing, so the original language stands")
}
