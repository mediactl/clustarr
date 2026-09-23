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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/quality"
)

func TestParseImportTarget(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    ImportTarget
		fileRef commonv1.MediaRef
	}{
		{"movie/the-matrix", ImportTarget{Kind: "movie", Name: "the-matrix"}, commonv1.MediaRef{Kind: "movie", Name: "the-matrix"}},
		{"album/ok-computer", ImportTarget{Kind: "album", Name: "ok-computer"}, commonv1.MediaRef{Kind: "album", Name: "ok-computer"}},
		{
			"comic/saga/saga-00001.0",
			ImportTarget{Kind: "comic", Name: "saga", Key: "saga-00001.0"},
			commonv1.MediaRef{Kind: "issue", Name: "saga-00001.0"},
		},
		{
			"series/bb/bb-s01e01",
			ImportTarget{Kind: "series", Name: "bb", Key: "bb-s01e01"},
			commonv1.MediaRef{Kind: "episode", Name: "bb-s01e01"},
		},
		{"issue/saga-00001.0", ImportTarget{Kind: "issue", Name: "saga-00001.0"}, commonv1.MediaRef{Kind: "issue", Name: "saga-00001.0"}},
	} {
		got, err := ParseImportTarget(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
		assert.Equal(t, tc.fileRef, got.FileRef(), tc.in)
		assert.Equal(t, tc.in, got.String(), "the grammar round-trips")
	}

	// Strict: nothing is repaired, because acting on a guessed reading of a
	// malformed instruction is the guess the never-guess rule forbids.
	for _, bad := range []string{
		"",
		"movie",
		"movie/",
		"/the-matrix",
		" movie/the-matrix",
		"movie/the-matrix ",
		"Movie/the-matrix",
		"film/the-matrix",
		"movie/The_Matrix",
		"movie/the-matrix/extra",  // a key on a kind with no keyed children
		"album/ok-computer/track", // likewise
		"comic/saga/Not Valid",
		"comic/saga/a/b",
	} {
		_, err := ParseImportTarget(bad)
		assert.Error(t, err, "%q must be rejected", bad)
	}
}

func TestParseImportOverride(t *testing.T) {
	v, err := ParseImportOverride("true")
	require.NoError(t, err)
	assert.True(t, v)
	v, err = ParseImportOverride("false")
	require.NoError(t, err)
	assert.False(t, v)
	for _, bad := range []string{"", "1", "t", "TRUE", "True", "yes", " true"} {
		_, err := ParseImportOverride(bad)
		assert.Error(t, err, "%q must be rejected, not read as a bool", bad)
	}
}

func TestReadDirectives(t *testing.T) {
	d, err := readDirectives(nil)
	require.NoError(t, err)
	assert.Nil(t, d.target)
	assert.False(t, d.override)

	d, err = readDirectives(map[string]string{AnnotationImportTarget: "book/dune", AnnotationImportOverride: "true"})
	require.NoError(t, err)
	require.NotNil(t, d.target)
	assert.Equal(t, commonv1.MediaKindBook, d.target.Kind)
	assert.True(t, d.override)

	_, err = readDirectives(map[string]string{AnnotationImportTarget: "book/dune", AnnotationImportOverride: "yes"})
	assert.Error(t, err, "a malformed override fails the pair, even with a good target")
}

func TestTargetFromSpec(t *testing.T) {
	assert.Equal(t, commonv1.MediaRef{Kind: "issue", Name: "saga-1"},
		targetFromSpec(commonv1.MediaRef{Kind: "comic", Name: "saga", Keys: []string{"saga-1"}}).FileRef())
	assert.Equal(t, commonv1.MediaRef{Kind: "comic", Name: "saga"},
		targetFromSpec(commonv1.MediaRef{Kind: "comic", Name: "saga", Keys: []string{"a", "b"}}).FileRef(),
		"a pack stays the container: choosing one of its issues would be a guess")
	assert.Equal(t, commonv1.MediaRef{Kind: "movie", Name: "m"},
		targetFromSpec(commonv1.MediaRef{Kind: "movie", Name: "m"}).FileRef())
}

func TestFileRefFitsRoot(t *testing.T) {
	assert.True(t, FileRefFitsRoot(commonv1.MediaRef{Kind: "album"}, catalogv1alpha1.RootFolderKindMusic))
	assert.True(t, FileRefFitsRoot(commonv1.MediaRef{Kind: "issue"}, catalogv1alpha1.RootFolderKindComic))
	assert.False(t, FileRefFitsRoot(commonv1.MediaRef{Kind: "book"}, catalogv1alpha1.RootFolderKindAudiobook))
	assert.False(t, FileRefFitsRoot(commonv1.MediaRef{Kind: "comic"}, catalogv1alpha1.RootFolderKindComic),
		"a comic holds no file of its own; its issues do")
}

func TestClassifyForAndFrozenQuality(t *testing.T) {
	// A 1 MiB ebook is a "sample" to fsops.Walk; ClassifyFor must not be.
	assert.Equal(t, fsops.ClassMedia, ClassifyFor(commonv1.MediaKindBook, "/l/A/B/A.epub"))
	assert.Equal(t, fsops.ClassMedia, ClassifyFor(commonv1.MediaKindAlbum, "/l/A/B/01.FLAC"))
	assert.Equal(t, fsops.ClassOther, ClassifyFor(commonv1.MediaKindAlbum, "/l/A/B/cover.jpg"))
	assert.Equal(t, fsops.ClassOther, ClassifyFor(commonv1.MediaKindBook, "/l/A/B/01.flac"), "the kind's own set only")
	assert.Equal(t, fsops.ClassSample, ClassifyFor(commonv1.MediaKindBook, "/l/A/B/sample.epub"))
	assert.Equal(t, fsops.ClassPart, ClassifyFor(commonv1.MediaKindIssue, "/l/A/B/x.cbz.part"))
	assert.Equal(t, fsops.ClassMedia, ClassifyFor(commonv1.MediaKindAlbum, "/l/Phish/Hoist/05 - Sample in a Jar.flac"),
		"music has no sample rule (pkg/fsops.IsSample): a track titled Sample is a track")
	assert.Equal(t, fsops.ClassMedia, ClassifyFor(commonv1.MediaKindAudiobook, "/l/A/B/Part 01.m4b"))
	assert.Equal(t, fsops.ClassOther, ClassifyFor(commonv1.MediaKindMovie, "/l/A/A.mkv"), "a video kind is not ClassifyFor's")

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
	assert.True(t, p.Create(event.CreateEvent{Object: dl(map[string]string{AnnotationImportTarget: "album/a"})}))
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: dl(nil), ObjectNew: dl(map[string]string{AnnotationImportOverride: "true"})}))
	assert.True(t, p.Update(event.UpdateEvent{
		ObjectOld: dl(map[string]string{AnnotationImportTarget: "album/a"}),
		ObjectNew: dl(map[string]string{AnnotationImportTarget: "album/b"}),
	}))
	assert.False(t, p.Update(event.UpdateEvent{
		ObjectOld: dl(map[string]string{AnnotationImportTarget: "album/a", "other": "1"}),
		ObjectNew: dl(map[string]string{AnnotationImportTarget: "album/a", "other": "2"}),
	}),
		"only the two import annotations re-trigger; a status write or another annotation must not loop it")
	assert.False(t, p.Delete(event.DeleteEvent{Object: dl(map[string]string{AnnotationImportTarget: "album/a"})}))
}

func TestRetriggerMessageID(t *testing.T) {
	a := RetriggerMessageID("ns", "dl", "uid", "album/a", "")
	assert.NotEqual(t, a, RetriggerMessageID("ns", "dl", "uid", "album/b", ""), "a changed instruction is a new message")
	assert.NotEqual(t, a, RetriggerMessageID("ns", "dl", "uid", "album/a", "true"))
	assert.Equal(t, a, RetriggerMessageID("ns", "dl", "uid", "album/a", ""), "the same instruction dedups")
	assert.NotEqual(t, "ns/dl:uid:import", a, "never grabarr's own completion message id")
}
