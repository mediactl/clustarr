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

package fsops_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
)

func TestIsPartMatchesTheAnacrolixPartSuffix(t *testing.T) {
	require.True(t, fsops.IsPart("../../testdata/fsops/classify/Movie.Title.2024.1080p.WEB-DL.mkv.part"))
	require.False(t, fsops.IsPart("../../testdata/fsops/classify/Movie.Title.2024.1080p.WEB-DL.mkv"))
}

func TestIsExtraMatchesAKnownExtrasFolder(t *testing.T) {
	require.True(t, fsops.IsExtra("../../testdata/fsops/classify/behind the scenes/short-clip.mkv"))
	require.True(t, fsops.IsExtra("../../testdata/fsops/classify/samples/Movie.Sample.mkv"),
		"a folder literally named samples is Jellyfin extras content")
	require.False(t, fsops.IsExtra("../../testdata/fsops/classify/Movie.Title.2024.1080p.WEB-DL.mkv"))
}

func TestIsSampleMatchesTheFilenameSignatureRegardlessOfSize(t *testing.T) {
	require.True(t, fsops.IsSample(fsops.KindVideo, "../../testdata/fsops/classify/Movie.Title.2024.Sample.mkv", 5*1024*1024*1024))
}

func TestIsSampleFlagsASmallMediaFileWithoutTheSignature(t *testing.T) {
	require.True(t, fsops.IsSample(fsops.KindVideo, "Movie.Title.2024.1080p.mkv", 10*1024*1024), "under the 50 MiB heuristic")
	require.True(t, fsops.IsSample(fsops.KindVideo, "Movie.Title.2024.1080p.mp4", 10*1024*1024), "every video container, not only .mkv")
	require.False(t, fsops.IsSample(fsops.KindVideo, "Movie.Title.2024.1080p.mkv", 2*1024*1024*1024), "a real-sized file is never a sample by size alone")
	require.False(t, fsops.IsSample(fsops.KindVideo, "readme.txt", 10), "size alone never flags a non-media extension")
	require.False(t, fsops.IsSample(fsops.KindVideo, "Movie.Title.2024.1080p.mkv", 0), "size 0 is unknown, never small")
}

const (
	mib = int64(1024 * 1024)
	gib = 1024 * mib
)

// TestClassifyRecognisesEveryKindsExtensions is the regression guard for
// the claim that every .mp4 movie and all audio was skipped: fsops knew
// only .mkv for video and no audio extension at all. Each file sits
// comfortably above the video sample floor for video, and well below it
// for every other kind, so the size rule cannot mask a missing extension
// in either direction.
func TestClassifyRecognisesEveryKindsExtensions(t *testing.T) {
	cases := map[fsops.Kind]struct {
		size  int64
		files []string
	}{
		fsops.KindVideo: {2 * gib, []string{
			"Movie.Title.2024.1080p.WEB-DL.mkv", "Movie.Title.2024.1080p.WEB-DL.mp4",
			"Movie.Title.2024.1080p.WEB-DL.MP4", "Movie.m4v", "Movie.avi", "Movie.mov",
			"Movie.wmv", "Movie.ts", "Movie.m2ts", "Movie.mpg", "Movie.mpeg", "Movie.webm",
		}},
		fsops.KindMusic: {8 * mib, []string{
			"01 - Airbag.mp3", "01 - Airbag.flac", "01 - Airbag.FLAC", "01 - Airbag.m4a",
			"01 - Airbag.aac", "01 - Airbag.ogg", "01 - Airbag.wv", "01 - Airbag.ape",
			"01 - Airbag.wav", "01 - Airbag.wma",
		}},
		fsops.KindAudiobook: {8 * mib, []string{"Book.m4b", "Book 01.mp3", "Book.flac", "Book.m4a"}},
		fsops.KindBook:      {mib, []string{"Book.epub", "Book.mobi", "Book.azw", "Book.azw3", "Book.pdf"}},
		fsops.KindComic:     {8 * mib, []string{"Saga 001.cbz", "Saga 001.cbr", "Saga 001.cb7", "Saga 001.cbt", "Saga 001.pdf"}},
	}
	for kind, tc := range cases {
		for _, f := range tc.files {
			assert.Equalf(t, fsops.ClassMedia, fsops.Classify(kind, "/lib/"+f, tc.size), "%s %s", kind, f)
		}
	}
}

func TestClassifyKeepsEachKindToItsOwnExtensions(t *testing.T) {
	assert.Equal(t, fsops.ClassOther, fsops.Classify(fsops.KindVideo, "/lib/Book.epub", 2*gib), "an ebook in a video library is not video")
	assert.Equal(t, fsops.ClassOther, fsops.Classify(fsops.KindVideo, "/lib/01.flac", 2*gib))
	assert.Equal(t, fsops.ClassOther, fsops.Classify(fsops.KindMusic, "/lib/Movie.mp4", 2*gib))
	assert.Equal(t, fsops.ClassOther, fsops.Classify(fsops.KindBook, "/lib/01.flac", mib))
	assert.Equal(t, fsops.ClassOther, fsops.Classify(fsops.KindMusic, "/lib/cover.jpg", mib))
	assert.Equal(t, fsops.ClassOther, fsops.Classify(fsops.Kind("series"), "/lib/Show.mkv", 2*gib), "an unknown kind has no media extensions")
}

// TestIsSampleSizeRuleIsVideoOnly: the 50 MiB floor must never apply to
// audio, ebooks or comics, where a small file is the normal case.
func TestIsSampleSizeRuleIsVideoOnly(t *testing.T) {
	for _, tc := range []struct {
		kind fsops.Kind
		path string
	}{
		{fsops.KindMusic, "01 - Airbag.flac"},
		{fsops.KindMusic, "01 - Airbag.mp3"},
		{fsops.KindAudiobook, "Book 01.mp3"},
		{fsops.KindAudiobook, "Book.m4b"},
		{fsops.KindBook, "Book.epub"},
		{fsops.KindComic, "Saga 001.cbz"},
	} {
		assert.Falsef(t, fsops.IsSample(tc.kind, tc.path, mib), "%s %s: a 1 MiB file is not a sample", tc.kind, tc.path)
		assert.Equalf(t, fsops.ClassMedia, fsops.Classify(tc.kind, tc.path, mib), "%s %s", tc.kind, tc.path)
	}
	assert.True(t, fsops.IsSample(fsops.KindVideo, "Movie.mkv", mib), "video keeps the floor")
}

func TestIsSampleNameRuleByKind(t *testing.T) {
	assert.True(t, fsops.IsSample(fsops.KindVideo, "Movie.Title.2024.Sample.mkv", 2*gib))
	assert.True(t, fsops.IsSample(fsops.KindBook, "Book (Sample).epub", mib))
	assert.True(t, fsops.IsSample(fsops.KindComic, "sample.cbz", mib))
	assert.True(t, fsops.IsSample(fsops.KindAudiobook, "Book - Sample.mp3", mib))
	assert.False(t, fsops.IsSample(fsops.KindMusic, "Phish - Sample in a Jar.flac", 30*mib),
		"a track titled Sample is a track: music has no sample rule")
	assert.False(t, fsops.IsSample(fsops.Kind("other"), "sample.mkv", mib), "an unknown kind has no sample rule")
}

// TestWalkAsClassifiesByKind walks one real tree per kind, with real sizes
// (sparse files), so the class fn receives is Classify's for that kind.
func TestWalkAsClassifiesByKind(t *testing.T) {
	root := t.TempDir()
	sparse := func(rel string, size int64) {
		t.Helper()
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		f, err := os.Create(p)
		require.NoError(t, err)
		require.NoError(t, f.Truncate(size))
		require.NoError(t, f.Close())
	}
	sparse("movie/Movie.Title.2024.1080p.WEB-DL.mp4", 60*mib)
	sparse("movie/Movie.Title.2024.Clip.mp4", 10*mib)
	sparse("album/01 - Airbag.flac", 30*mib)
	sparse("album/02 - Paranoid Android.mp3", 7*mib)
	sparse("album/cover.jpg", mib)
	sparse("books/Dune.epub", mib)

	walk := func(kind fsops.Kind, dir string, viaWalk bool) map[string]fsops.FileClass {
		t.Helper()
		got := map[string]fsops.FileClass{}
		fn := func(path string, _ os.FileInfo, class fsops.FileClass) error {
			got[filepath.Base(path)] = class
			return nil
		}
		var err error
		if viaWalk {
			err = fsops.Walk(context.Background(), filepath.Join(root, dir), fn)
		} else {
			err = fsops.WalkAs(context.Background(), kind, filepath.Join(root, dir), fn)
		}
		require.NoError(t, err)
		return got
	}

	assert.Equal(t, map[string]fsops.FileClass{
		"Movie.Title.2024.1080p.WEB-DL.mp4": fsops.ClassMedia,
		"Movie.Title.2024.Clip.mp4":         fsops.ClassSample,
	}, walk(fsops.KindVideo, "movie", true), "Walk classifies as video")
	assert.Equal(t, map[string]fsops.FileClass{
		"01 - Airbag.flac":          fsops.ClassMedia,
		"02 - Paranoid Android.mp3": fsops.ClassMedia,
		"cover.jpg":                 fsops.ClassOther,
	}, walk(fsops.KindMusic, "album", false))
	assert.Equal(t, map[string]fsops.FileClass{"Dune.epub": fsops.ClassMedia}, walk(fsops.KindBook, "books", false))
	assert.Equal(t, map[string]fsops.FileClass{"Dune.epub": fsops.ClassOther}, walk(fsops.KindVideo, "books", true))
}

func TestWalkClassifiesTheFixtureTree(t *testing.T) {
	got := map[string]fsops.FileClass{}
	err := fsops.Walk(context.Background(), "../../testdata/fsops/classify", func(path string, info os.FileInfo, class fsops.FileClass) error {
		rel, relErr := filepath.Rel("../../testdata/fsops/classify", path)
		require.NoError(t, relErr)
		got[filepath.ToSlash(rel)] = class
		return nil
	})
	require.NoError(t, err)

	require.Equal(t, map[string]fsops.FileClass{
		"Movie.Title.2024.1080p.WEB-DL.mkv":      fsops.ClassMedia,
		"Movie.Title.2024.1080p.WEB-DL.mkv.part": fsops.ClassPart,
		"Movie.Title.2024.Sample.mkv":            fsops.ClassSample,
		"behind the scenes/short-clip.mkv":       fsops.ClassExtra,
		"samples/Movie.Sample.mkv":               fsops.ClassExtra,
		"notes.txt":                              fsops.ClassOther,
	}, got)
}

func TestWalkStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := fsops.Walk(ctx, "../../testdata/fsops/classify", func(string, os.FileInfo, fsops.FileClass) error {
		t.Fatal("fn must not be called once ctx is already cancelled")
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
}
