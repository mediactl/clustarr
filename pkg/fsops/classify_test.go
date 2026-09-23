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

const fixtureRoot = "../../testdata/fsops/classify"

func TestIsExtraMatchesAKnownExtrasFolder(t *testing.T) {
	require.True(t, fsops.IsExtra(fsops.KindVideo, fixtureRoot, fixtureRoot+"/behind the scenes/short-clip.mkv"))
	require.True(t, fsops.IsExtra(fsops.KindVideo, fixtureRoot, fixtureRoot+"/samples/Movie.Sample.mkv"),
		"a folder literally named samples is Jellyfin extras content")
	require.False(t, fsops.IsExtra(fsops.KindVideo, fixtureRoot, fixtureRoot+"/Movie.Title.2024.1080p.WEB-DL.mkv"))
}

// TestIsExtraIsBoundedToTheRoot: the folders above the root -- and the
// root's own name -- are the operator's layout, not the release's. Before
// the bound, IsExtra walked every parent up to "/", so a whole library under
// a directory named Extras was skipped file by file.
func TestIsExtraIsBoundedToTheRoot(t *testing.T) {
	for _, tc := range []struct {
		root, path string
		want       bool
		why        string
	}{
		{"/mnt/Extras/movies", "/mnt/Extras/movies/Heat (1995)/Heat (1995).mkv", false, "a root under a directory named Extras holds movies"},
		{"/mnt/Extras/movies", "/mnt/Extras/movies/Heat (1995)/Extras/Making Of.mkv", true, "an extras folder beneath the root is still an extra"},
		{"/mnt/Extras/movies", "/mnt/Extras/movies/Heat (1995)/featurettes/deep/x.mkv", true, "at any depth beneath the root"},
		{"/lib/Extras", "/lib/Extras/Heat (1995)/Heat (1995).mkv", false, "the root's own name is not consulted"},
		{"/lib/Trailers/", "/lib/Trailers/Heat.mkv", false, "a trailing slash on the root changes nothing"},
		{"/lib/movies", "/lib/Extras/Heat.mkv", false, "a path outside the root has no folder that can be judged"},
		{"", "/lib/Extras/Heat.mkv", false, "an empty root bounds everything out"},
		{"/lib/movies", "/lib/movies/Heat.mkv", false, "a file directly in the root"},
	} {
		assert.Equalf(t, tc.want, fsops.IsExtra(fsops.KindVideo, tc.root, tc.path), "%s: root %q path %q", tc.why, tc.root, tc.path)
	}
}

// TestIsExtraIsVideoOnly: the list is Jellyfin's movie convention. An album
// or a book in a folder named "Interviews" or "Extras" is the album or book.
func TestIsExtraIsVideoOnly(t *testing.T) {
	for _, tc := range []struct {
		kind fsops.Kind
		path string
	}{
		{fsops.KindMusic, "/lib/Miles Davis/Interviews/01 - Part One.flac"},
		{fsops.KindMusic, "/lib/Radiohead/OK Computer/Extras/12 - Lucky (Live).mp3"},
		{fsops.KindAudiobook, "/lib/Studs Terkel/Interviews/Part 01.m4b"},
		{fsops.KindBook, "/lib/Frank Herbert/Extras/Dune.epub"},
		{fsops.KindComic, "/lib/Saga/Extras/Saga 001.cbz"},
	} {
		assert.Falsef(t, fsops.IsExtra(tc.kind, "/lib", tc.path), "%s %s", tc.kind, tc.path)
		c := fsops.Classifier{Kind: tc.kind, Root: "/lib", SampleMaxBytes: fsops.DefaultSampleMaxBytes}
		assert.Equalf(t, fsops.ClassMedia, c.Classify(tc.path, mib), "%s %s", tc.kind, tc.path)
	}
	assert.True(t, fsops.IsExtra(fsops.KindVideo, "/lib", "/lib/Heat (1995)/Interviews/Mann.mkv"), "video keeps the list")
}

func TestIsSampleMatchesTheFilenameSignature(t *testing.T) {
	require.True(t, fsops.IsSample(fsops.KindVideo, fixtureRoot+"/Movie.Title.2024.Sample.mkv"))
	require.False(t, fsops.IsSample(fsops.KindVideo, "Movie.Title.2024.1080p.mkv"), "the name rule looks at the name only")
}

func TestIsSuspectedSampleFlagsASmallVideoFileWithoutTheSignature(t *testing.T) {
	def := fsops.DefaultSampleMaxBytes
	require.True(t, fsops.IsSuspectedSample(fsops.KindVideo, "Movie.Title.2024.1080p.mkv", 10*mib, def), "under the 50 MiB heuristic")
	require.True(t, fsops.IsSuspectedSample(fsops.KindVideo, "Movie.Title.2024.1080p.mp4", 10*mib, def), "every video container, not only .mkv")
	require.False(t, fsops.IsSuspectedSample(fsops.KindVideo, "Movie.Title.2024.1080p.mkv", 2*gib, def), "a real-sized file is never a sample by size alone")
	require.False(t, fsops.IsSuspectedSample(fsops.KindVideo, "Movie.Title.2024.1080p.mkv", def, def), "the threshold itself is media")
	require.False(t, fsops.IsSuspectedSample(fsops.KindVideo, "readme.txt", 10, def), "size alone never flags a non-media extension")
	require.False(t, fsops.IsSuspectedSample(fsops.KindVideo, "Movie.Title.2024.1080p.mkv", 0, def), "size 0 is unknown, never small")
	require.False(t, fsops.IsSuspectedSample(fsops.KindVideo, "Movie.Title.2024.1080p.mkv", 10*mib, 0), "a zero threshold disables the rule")
	require.False(t, fsops.IsSuspectedSample(fsops.KindVideo, "Movie.Title.2024.1080p.mkv", 10*mib, -1), "so does a negative one")
	require.True(t, fsops.IsSuspectedSample(fsops.KindVideo, "Movie.mkv", 10*mib, 20*mib), "the threshold is the caller's")
	require.False(t, fsops.IsSuspectedSample(fsops.KindVideo, "Movie.mkv", 30*mib, 20*mib))
}

// TestClassifySeparatesTheSizeSuspicionFromTheNameVerdict is the fsops half
// of the never-guess fix: a small video whose name does not say "sample" is
// its own class, ClassSuspectedSample, so a caller can surface it instead of
// skipping it alongside the name-marked samples.
func TestClassifySeparatesTheSizeSuspicionFromTheNameVerdict(t *testing.T) {
	on := fsops.Classifier{Kind: fsops.KindVideo, Root: "/lib", SampleMaxBytes: fsops.DefaultSampleMaxBytes}
	off := fsops.Classifier{Kind: fsops.KindVideo, Root: "/lib"}

	assert.Equal(t, fsops.ClassSuspectedSample, on.Classify("/lib/Short Film (2019)/Short Film (2019).mkv", 45*mib))
	assert.Equal(t, fsops.ClassMedia, off.Classify("/lib/Short Film (2019)/Short Film (2019).mkv", 45*mib),
		"SampleMaxBytes 0 disables the size rule")
	assert.Equal(t, fsops.ClassMedia, on.Classify("/lib/Heat (1995)/Heat (1995).mkv", 2*gib))

	assert.Equal(t, fsops.ClassSample, on.Classify("/lib/Heat (1995)/heat-sample.mkv", 45*mib), "the name wins over the size")
	assert.Equal(t, fsops.ClassSample, off.Classify("/lib/Heat (1995)/heat-sample.mkv", 45*mib),
		"disabling the size rule leaves the name rule alone")
	assert.Equal(t, fsops.ClassSample, on.Classify("/lib/Heat (1995)/heat-sample.mkv", 2*gib), "at any size")

	assert.Equal(t, fsops.ClassExtra, on.Classify("/lib/Heat (1995)/Trailers/Heat.mkv", 45*mib), "an extras folder wins over both")
	assert.Equal(t, fsops.ClassPart, on.Classify("/lib/Heat (1995)/Heat.mkv.part", 45*mib))
	assert.Equal(t, "suspected-sample", fsops.ClassSuspectedSample.String())
}

const (
	mib = int64(1024 * 1024)
	gib = 1024 * mib
)

// classifier is kind's Classifier over /lib with the production threshold.
func classifier(kind fsops.Kind) fsops.Classifier {
	return fsops.Classifier{Kind: kind, Root: "/lib", SampleMaxBytes: fsops.DefaultSampleMaxBytes}
}

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
			assert.Equalf(t, fsops.ClassMedia, classifier(kind).Classify("/lib/"+f, tc.size), "%s %s", kind, f)
		}
	}
}

func TestClassifyKeepsEachKindToItsOwnExtensions(t *testing.T) {
	assert.Equal(t, fsops.ClassOther, classifier(fsops.KindVideo).Classify("/lib/Book.epub", 2*gib), "an ebook in a video library is not video")
	assert.Equal(t, fsops.ClassOther, classifier(fsops.KindVideo).Classify("/lib/01.flac", 2*gib))
	assert.Equal(t, fsops.ClassOther, classifier(fsops.KindMusic).Classify("/lib/Movie.mp4", 2*gib))
	assert.Equal(t, fsops.ClassOther, classifier(fsops.KindBook).Classify("/lib/01.flac", mib))
	assert.Equal(t, fsops.ClassOther, classifier(fsops.KindMusic).Classify("/lib/cover.jpg", mib))
	assert.Equal(t, fsops.ClassOther, classifier(fsops.Kind("series")).Classify("/lib/Show.mkv", 2*gib), "an unknown kind has no media extensions")
}

// TestSizeRuleIsVideoOnly: the 50 MiB floor must never apply to audio,
// ebooks or comics, where a small file is the normal case.
func TestSizeRuleIsVideoOnly(t *testing.T) {
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
		assert.Falsef(t, fsops.IsSuspectedSample(tc.kind, tc.path, mib, fsops.DefaultSampleMaxBytes),
			"%s %s: a 1 MiB file is not a sample", tc.kind, tc.path)
		assert.Equalf(t, fsops.ClassMedia, classifier(tc.kind).Classify(tc.path, mib), "%s %s", tc.kind, tc.path)
	}
	assert.True(t, fsops.IsSuspectedSample(fsops.KindVideo, "Movie.mkv", mib, fsops.DefaultSampleMaxBytes), "video keeps the floor")
}

func TestIsSampleNameRuleByKind(t *testing.T) {
	assert.True(t, fsops.IsSample(fsops.KindVideo, "Movie.Title.2024.Sample.mkv"))
	assert.True(t, fsops.IsSample(fsops.KindBook, "Book (Sample).epub"))
	assert.True(t, fsops.IsSample(fsops.KindComic, "sample.cbz"))
	assert.True(t, fsops.IsSample(fsops.KindAudiobook, "Book - Sample.mp3"))
	assert.False(t, fsops.IsSample(fsops.KindMusic, "Phish - Sample in a Jar.flac"),
		"a track titled Sample is a track: music has no sample rule")
	assert.False(t, fsops.IsSample(fsops.Kind("other"), "sample.mkv"), "an unknown kind has no sample rule")
}

// sparseTree plants sparse files of the given sizes under a fresh temp dir
// and returns it.
func sparseTree(t *testing.T, files map[string]int64) string {
	t.Helper()
	root := t.TempDir()
	for rel, size := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		f, err := os.Create(p)
		require.NoError(t, err)
		require.NoError(t, f.Truncate(size))
		require.NoError(t, f.Close())
	}
	return root
}

// walk runs c.Walk over dir and returns each file's class, keyed by its
// path relative to dir.
func walk(t *testing.T, c fsops.Classifier, dir string) map[string]fsops.FileClass {
	t.Helper()
	got := map[string]fsops.FileClass{}
	err := c.Walk(context.Background(), dir, func(path string, _ os.FileInfo, class fsops.FileClass) error {
		rel, err := filepath.Rel(dir, path)
		require.NoError(t, err)
		got[filepath.ToSlash(rel)] = class
		return nil
	})
	require.NoError(t, err)
	return got
}

// TestWalkClassifiesByKind walks one real tree per kind, with real sizes
// (sparse files), so the class fn receives is Classify's for that kind.
func TestWalkClassifiesByKind(t *testing.T) {
	root := sparseTree(t, map[string]int64{
		"movie/Movie.Title.2024.1080p.WEB-DL.mp4": 60 * mib,
		"movie/Movie.Title.2024.Clip.mp4":         10 * mib,
		"album/01 - Airbag.flac":                  30 * mib,
		"album/02 - Paranoid Android.mp3":         7 * mib,
		"album/cover.jpg":                         mib,
		"books/Dune.epub":                         mib,
	})
	with := func(kind fsops.Kind) fsops.Classifier {
		return fsops.Classifier{Kind: kind, Root: root, SampleMaxBytes: fsops.DefaultSampleMaxBytes}
	}

	assert.Equal(t, map[string]fsops.FileClass{
		"Movie.Title.2024.1080p.WEB-DL.mp4": fsops.ClassMedia,
		"Movie.Title.2024.Clip.mp4":         fsops.ClassSuspectedSample,
	}, walk(t, with(fsops.KindVideo), filepath.Join(root, "movie")))
	assert.Equal(t, map[string]fsops.FileClass{
		"01 - Airbag.flac":          fsops.ClassMedia,
		"02 - Paranoid Android.mp3": fsops.ClassMedia,
		"cover.jpg":                 fsops.ClassOther,
	}, walk(t, with(fsops.KindMusic), filepath.Join(root, "album")))
	assert.Equal(t, map[string]fsops.FileClass{"Dune.epub": fsops.ClassMedia}, walk(t, with(fsops.KindBook), filepath.Join(root, "books")))
	assert.Equal(t, map[string]fsops.FileClass{"Dune.epub": fsops.ClassOther}, walk(t, with(fsops.KindVideo), filepath.Join(root, "books")))
}

// TestWalkUnderADirectoryNamedExtras is IsExtra's bound on a real tree: a
// library whose root folder sits under /…/Extras/ classifies its movies as
// media, and only a real extras folder beneath the root is an extra. A walk
// narrowed to one movie's folder (a LibraryScan subpath) classifies the same
// way, because the bound is the Classifier's Root, not the walked dir.
func TestWalkUnderADirectoryNamedExtras(t *testing.T) {
	tmp := sparseTree(t, map[string]int64{
		"Extras/movies/Heat (1995)/Heat (1995).mkv":              2 * gib,
		"Extras/movies/Heat (1995)/Featurettes/Making Heat.mkv":  2 * gib,
		"Extras/music/Miles Davis/Interviews/01 - Part One.flac": 30 * mib,
	})
	movies := fsops.Classifier{Kind: fsops.KindVideo, Root: filepath.Join(tmp, "Extras", "movies"), SampleMaxBytes: fsops.DefaultSampleMaxBytes}
	want := map[string]fsops.FileClass{
		"Heat (1995)/Heat (1995).mkv":             fsops.ClassMedia,
		"Heat (1995)/Featurettes/Making Heat.mkv": fsops.ClassExtra,
	}
	assert.Equal(t, want, walk(t, movies, movies.Root))

	narrowed := walk(t, movies, filepath.Join(movies.Root, "Heat (1995)"))
	assert.Equal(t, map[string]fsops.FileClass{
		"Heat (1995).mkv":             fsops.ClassMedia,
		"Featurettes/Making Heat.mkv": fsops.ClassExtra,
	}, narrowed)

	music := fsops.Classifier{Kind: fsops.KindMusic, Root: filepath.Join(tmp, "Extras", "music")}
	assert.Equal(t, map[string]fsops.FileClass{
		"Miles Davis/Interviews/01 - Part One.flac": fsops.ClassMedia,
	}, walk(t, music, music.Root), "an album in a folder named Interviews, in a root under Extras, is an album")
}

func TestWalkClassifiesTheFixtureTree(t *testing.T) {
	c := fsops.Classifier{Kind: fsops.KindVideo, Root: fixtureRoot, SampleMaxBytes: fsops.DefaultSampleMaxBytes}
	require.Equal(t, map[string]fsops.FileClass{
		"Movie.Title.2024.1080p.WEB-DL.mkv":      fsops.ClassMedia,
		"Movie.Title.2024.1080p.WEB-DL.mkv.part": fsops.ClassPart,
		"Movie.Title.2024.Sample.mkv":            fsops.ClassSample,
		"behind the scenes/short-clip.mkv":       fsops.ClassExtra,
		"samples/Movie.Sample.mkv":               fsops.ClassExtra,
		"notes.txt":                              fsops.ClassOther,
	}, walk(t, c, fixtureRoot))
}

func TestWalkStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := fsops.Classifier{Kind: fsops.KindVideo, Root: fixtureRoot}
	err := c.Walk(ctx, fixtureRoot, func(string, os.FileInfo, fsops.FileClass) error {
		t.Fatal("fn must not be called once ctx is already cancelled")
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
}
