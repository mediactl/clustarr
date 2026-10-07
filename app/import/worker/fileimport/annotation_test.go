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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importtarget"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/quality"
)

func TestClassifierForAndFrozenQuality(t *testing.T) {
	const mib = int64(1 << 20)
	classify := func(kind commonv1.MediaKind, path string, size int64) fsops.FileClass {
		return ClassifierFor(kind, "/l", fsops.DefaultSampleMaxBytes).Classify(path, size)
	}
	// A 1 MiB ebook is under the video sample floor; a book is not video.
	assert.Equal(t, fsops.ClassMedia, classify(commonv1.MediaKindBook, "/l/A/B/A.epub", mib))
	assert.Equal(t, fsops.ClassMedia, classify(commonv1.MediaKindAlbum, "/l/A/B/01.FLAC", mib))
	assert.Equal(t, fsops.ClassOther, classify(commonv1.MediaKindAlbum, "/l/A/B/cover.jpg", mib))
	assert.Equal(t, fsops.ClassOther, classify(commonv1.MediaKindBook, "/l/A/B/01.flac", mib), "the kind's own set only")
	assert.Equal(t, fsops.ClassSample, classify(commonv1.MediaKindBook, "/l/A/B/sample.epub", mib))
	assert.Equal(t, fsops.ClassPart, classify(commonv1.MediaKindIssue, "/l/A/B/x.cbz.part", mib))
	assert.Equal(t, fsops.ClassMedia, classify(commonv1.MediaKindAlbum, "/l/Phish/Hoist/05 - Sample in a Jar.flac", mib),
		"music has no sample rule (pkg/fsops.IsSample): a track titled Sample is a track")
	assert.Equal(t, fsops.ClassMedia, classify(commonv1.MediaKindAudiobook, "/l/A/B/Part 01.m4b", mib))
	assert.Equal(t, fsops.ClassMedia, classify(commonv1.MediaKindAlbum, "/l/Miles Davis/Interviews/01.flac", mib),
		"the extras-folder list is video's alone")
	assert.Equal(t, fsops.ClassMedia, classify(commonv1.MediaKindMovie, "/l/A/A.mkv", 60*mib), "a movie is video")
	assert.Equal(t, fsops.ClassSuspectedSample, classify(commonv1.MediaKindMovie, "/l/A/A.mkv", mib), "with video's size floor")
	assert.Equal(t, fsops.ClassExtra, classify(commonv1.MediaKindMovie, "/l/A/Extras/A.mkv", 60*mib), "and video's extras folders")
	assert.Equal(t, fsops.ClassMedia, ClassifierFor(commonv1.MediaKindMovie, "/l", 0).Classify("/l/A/A.mkv", mib),
		"a zero threshold turns the size floor off")

	for _, tc := range []struct {
		kind commonv1.MediaKind
		path string
		want string // "" = unknown
	}{
		{commonv1.MediaKindBook, "x.EPUB", "EPUB"},
		{commonv1.MediaKindBook, "x.azw", ""},
		{commonv1.MediaKindIssue, "x.cbz", "CBZ"},
		{commonv1.MediaKindIssue, "x.cb7", ""},
		{commonv1.MediaKindAudiobook, "x.m4b", "M4B"},
		{commonv1.MediaKindAudiobook, "x.m4a", "Unknown Audio"}, // the ladder's own tier for an audio format outside it
		{commonv1.MediaKindAlbum, "x.wav", "WAV"},
		{commonv1.MediaKindAlbum, "x.flac", "FLAC"}, // Lidarr: FLAC unless a 24-bit marker is declared
		{commonv1.MediaKindAlbum, "x.ape", "FLAC"},  // Lidarr's Lossless group
		{commonv1.MediaKindAlbum, "x.mp3", ""},      // bitrate bands: needs a probe
		{commonv1.MediaKindAlbum, "x.m4a", ""},      // ALAC or AAC
	} {
		q, ok := FrozenQuality(tc.kind, tc.path)
		assert.Equal(t, tc.want != "", ok, "%s %s", tc.kind, tc.path)
		assert.Equal(t, tc.want, q.Name, "%s %s", tc.kind, tc.path)
		if ok {
			def, found := quality.Lookup(ProfileKindFor(tc.kind), tc.want)
			require.True(t, found)
			assert.Equal(t, def.Quality, q, "frozen verbatim from pkg/quality's definition")
		}
	}

	for _, declared := range []string{"Radiohead - OK Computer (1997) [FLAC 24bit]", "Artist-Album-24BIT-WEB-FLAC-2016-GRP", "x/Album (24-bit)/01.flac"} {
		q, _ := FrozenQuality(commonv1.MediaKindAlbum, "01.flac", "", declared)
		assert.Equal(t, "24bit Lossless", q.Name, declared)
	}
	for _, declared := range []string{"Album [FLAC 16bit]", "Album 124bit", "Album 24bitrate"} {
		q, _ := FrozenQuality(commonv1.MediaKindAlbum, "01.flac", declared)
		assert.Equal(t, "FLAC", q.Name, declared)
	}
	q, _ := FrozenQuality(commonv1.MediaKindAlbum, "01.wav", "24bit")
	assert.Equal(t, "WAV", q.Name, "the marker only splits the lossless tier")
}

// A release date is read in UTC. metav1.Time decodes into the replica's
// local zone, so west of UTC a 1 January 00:30 UTC release is 31 December of
// the previous year locally.
func TestReleaseYearIsUTC(t *testing.T) {
	west := time.FixedZone("UTC-8", -8*3600)
	jan1 := metav1.NewTime(time.Date(1997, 1, 1, 0, 30, 0, 0, time.UTC).In(west))
	require.Equal(t, 1996, jan1.Year(), "the premise: the local calendar says 1996")
	assert.Equal(t, 1997, ReleaseYear(&jan1))
	assert.Zero(t, ReleaseYear(nil))
	assert.Zero(t, ReleaseYear(&metav1.Time{}))
}

func TestWellFormedFolder(t *testing.T) {
	assert.True(t, wellFormedFolder("Frank Herbert/Dune/2 - 1969 - Dune Messiah Scott Brick"))
	assert.False(t, wellFormedFolder("Frank Herbert// -  - Dune"), "pkg/naming's render with no series, position or year")
	assert.False(t, wellFormedFolder(""))
}

func TestImportAnnotationsChanged(t *testing.T) {
	p := ImportAnnotationsChanged()
	dl := func(ann map[string]string) *downloadv1alpha1.Download {
		return &downloadv1alpha1.Download{ObjectMeta: metav1.ObjectMeta{Annotations: ann}}
	}
	assert.False(t, p.Create(event.CreateEvent{Object: dl(nil)}))
	assert.True(t, p.Create(event.CreateEvent{Object: dl(map[string]string{importtarget.AnnotationImportTarget: "album/a"})}))
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: dl(nil), ObjectNew: dl(map[string]string{importtarget.AnnotationImportOverride: "true"})}))
	assert.True(t, p.Update(event.UpdateEvent{
		ObjectOld: dl(map[string]string{importtarget.AnnotationImportTarget: "album/a"}),
		ObjectNew: dl(map[string]string{importtarget.AnnotationImportTarget: "album/b"}),
	}))
	assert.False(t, p.Update(event.UpdateEvent{
		ObjectOld: dl(map[string]string{importtarget.AnnotationImportTarget: "album/a", "other": "1"}),
		ObjectNew: dl(map[string]string{importtarget.AnnotationImportTarget: "album/a", "other": "2"}),
	}),
		"only the two import annotations re-trigger; a status write or another annotation must not loop it")
	assert.False(t, p.Delete(event.DeleteEvent{Object: dl(map[string]string{importtarget.AnnotationImportTarget: "album/a"})}))
}

func TestRetriggerMessageID(t *testing.T) {
	a := RetriggerMessageID("ns", "dl", "uid", "album/a", "")
	assert.NotEqual(t, a, RetriggerMessageID("ns", "dl", "uid", "album/b", ""), "a changed instruction is a new message")
	assert.NotEqual(t, a, RetriggerMessageID("ns", "dl", "uid", "album/a", "true"))
	assert.Equal(t, a, RetriggerMessageID("ns", "dl", "uid", "album/a", ""), "the same instruction dedups")
	assert.NotEqual(t, "ns/dl:uid:import", a, "never grabarr's own completion message id")
}
