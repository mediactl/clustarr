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

package views_test

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, c.Render(context.Background(), &b))
	return b.String()
}

// cells are a rendered row's <td> texts, tags stripped.
func cells(html string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`).FindAllStringSubmatch(html, -1) {
		out = append(out, strings.TrimSpace(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(m[1], "")))
	}
	return out
}

func heads(html string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`(?s)<th[^>]*>(.*?)</th>`).FindAllStringSubmatch(html, -1) {
		out = append(out, strings.TrimSpace(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(m[1], "")))
	}
	return out
}

// TestFileCellsPerKind: the Files table's columns follow the file's kind
// (2026-11-10 design §9.6) -- a movie's or episode's file (and a row with no
// kind, as the movie page's always were) has Radarr's video columns, an
// album's or audiobook's file the audio columns (codec, bitrate, sample
// rate with the bit depth of a lossless track, size), a book's or issue's
// the format and pages -- every kind keeping the frozen quality, release
// group, formats and score, and the header's cells matching the row's one
// for one.
func TestFileCellsPerKind(t *testing.T) {
	base := views.FileRow{
		Name: "f", RelativePath: "a/b.ext", SizeBytes: 2 << 30, Quality: "Q", ReleaseGroup: "GRP", Formats: []string{"F1"}, Score: 7,
		VideoCodec: "HEVC", Audio: "EAC3 - 5.1", Languages: []string{"en"}, Markers: "intro 0:00–1:00 (chapters)", Transcode: "Succeeded",
		AudioCodec: "FLAC", BitrateKbps: 1411, SampleRateHz: 44100, BitDepth: 16, Format: "EPUB", Pages: 312,
	}
	for _, tc := range []struct {
		kind  commonv1.MediaKind
		heads []string
		cells []string
	}{
		{"", []string{"Relative Path", "Video Codec", "Audio Info", "Size", "Languages", "Quality", "Release Group", "Formats", "Markers", "Transcode", "Score"},
			[]string{"a/b.ext", "HEVC", "EAC3 - 5.1", "2.00 GiB", "en", "Q", "GRP", "F1", "intro 0:00–1:00 (chapters)", "Succeeded", "7"}},
		{commonv1.MediaKindMovie, nil, []string{"a/b.ext", "HEVC", "EAC3 - 5.1", "2.00 GiB", "en", "Q", "GRP", "F1", "intro 0:00–1:00 (chapters)", "Succeeded", "7"}},
		{commonv1.MediaKindEpisode, nil, []string{"a/b.ext", "HEVC", "EAC3 - 5.1", "2.00 GiB", "en", "Q", "GRP", "F1", "intro 0:00–1:00 (chapters)", "Succeeded", "7"}},
		{commonv1.MediaKindAlbum, []string{"Relative Path", "Codec", "Bitrate", "Sample Rate", "Size", "Quality", "Release Group", "Formats", "Score"},
			[]string{"a/b.ext", "FLAC", "1411 kbps", "44.1 kHz / 16-bit", "2.00 GiB", "Q", "GRP", "F1", "7"}},
		{commonv1.MediaKindAudiobook, nil, []string{"a/b.ext", "FLAC", "1411 kbps", "44.1 kHz / 16-bit", "2.00 GiB", "Q", "GRP", "F1", "7"}},
		{commonv1.MediaKindBook, []string{"Relative Path", "Format", "Pages", "Size", "Quality", "Release Group", "Formats", "Score"},
			[]string{"a/b.ext", "EPUB", "312", "2.00 GiB", "Q", "GRP", "F1", "7"}},
		{commonv1.MediaKindIssue, nil, []string{"a/b.ext", "EPUB", "312", "2.00 GiB", "Q", "GRP", "F1", "7"}},
	} {
		row := base
		row.Kind = tc.kind
		got := cells(render(t, views.FileCells(row)))
		require.Equal(t, tc.cells, got, "%q's cells", tc.kind)
		hs := heads(render(t, views.FileHeads(tc.kind)))
		require.Len(t, hs, len(got), "%q's header has a cell per column", tc.kind)
		if tc.heads != nil {
			require.Equal(t, tc.heads, hs, "%q's header", tc.kind)
		}
	}
	// A lossy track has no bit depth, an unprobed one no rate or bitrate;
	// an unknown page count or format is blank.
	lossy := base
	lossy.Kind, lossy.AudioCodec, lossy.BitrateKbps, lossy.SampleRateHz, lossy.BitDepth = commonv1.MediaKindAlbum, "MP3", 320, 48000, 0
	require.Equal(t, []string{"MP3", "320 kbps", "48 kHz"}, cells(render(t, views.FileCells(lossy)))[1:4])
	unprobed := views.FileRow{Kind: commonv1.MediaKindAudiobook, RelativePath: "x.m4b"}
	require.Equal(t, []string{"x.m4b", "", "", "", "0 B", "", "", "", "0"}, cells(render(t, views.FileCells(unprobed))))
	blank := views.FileRow{Kind: commonv1.MediaKindIssue, RelativePath: "x.cbz"}
	require.Equal(t, []string{"x.cbz", "", "", "0 B", "", "", "", "0"}, cells(render(t, views.FileCells(blank))))
	require.Equal(t, commonv1.MediaKindBook, views.FilesKind([]views.FileRow{{Kind: commonv1.MediaKindBook}}))
	require.Empty(t, views.FilesKind(nil))
}

// TestItemActionsComposeByName: a page composes its action bar from the
// shared buttons by name (2026-11-10 design §9.6), in the order it asks,
// each rendered only when the item takes it: Refresh reads "Refresh & Scan"
// for an item whose folder is on disk (views.Detail.HasFolder, beside
// CanScan, which Editable reads), Rename appears only then, the interactive
// search posts to the item's route and opens the kind's own modal region,
// Edit and Images come only for the kinds their dialogs know, and Delete
// only for a deletable kind.
func TestItemActionsComposeByName(t *testing.T) {
	audiobook := views.Detail{Item: projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "media", Name: "dune"}, Kind: commonv1.MediaKindAudiobook,
		Tab: projection.TabAudiobooks, Title: "Dune", Monitored: true,
	}, CanScan: true, HasFolder: true}
	html := render(t, views.ItemActions(audiobook, []views.Action{views.ActionRefresh, views.ActionSearch, views.ActionInteractive, views.ActionRename, views.ActionImages, views.ActionDelete}))
	actions := regexp.MustCompile(`data-action="([^"]+)"`).FindAllStringSubmatch(html, -1)
	var names []string
	for _, m := range actions {
		names = append(names, m[1])
	}
	require.Equal(t, []string{"refresh-metadata", "search-now", "audiobook-interactive-search", "rename-dry-run", "rename-apply", "delete", "delete-confirm"}, names,
		"in the order asked; no Images for an audiobook (the dialog knows movies and series), Delete (its trigger and its dialog's confirm) for a deletable kind")
	require.Contains(t, html, `title="Refresh &amp; Scan"`)
	require.Contains(t, html, `title="Search Audiobook"`)
	require.Contains(t, html, `action="/library/media/audiobook/dune/search/interactive"`)
	require.Contains(t, html, `hx-target="#audiobook-modal"`)
	require.Contains(t, html, `data-toolbar`, "it is the item page's bar")

	// Without a folder: Refresh alone, no Rename; a page asking for fewer
	// buttons gets fewer.
	issue := views.Detail{Item: projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "media", Name: "saga-1"}, Kind: commonv1.MediaKindIssue, Tab: projection.TabComics, Title: "Saga #1",
	}}
	html = render(t, views.ItemActions(issue, []views.Action{views.ActionRename, views.ActionInteractive, views.ActionRefresh}))
	names = nil
	for _, m := range regexp.MustCompile(`data-action="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		names = append(names, m[1])
	}
	require.Equal(t, []string{"issue-interactive-search", "refresh-metadata"}, names)
	require.Contains(t, html, `<span>Refresh</span>`)
	require.NotContains(t, html, `Refresh &amp; Scan`, "no folder, no scan; and a title equal to the word is dropped (actionTitle)")
	require.Contains(t, html, `hx-target="#issue-modal"`)
	require.Equal(t, "#album-modal", views.ModalRegion(commonv1.MediaKindAlbum))

	// The item pages' default composition is every action; a series' and a
	// container's bar still shows no interactive search of its own.
	movie := views.Detail{Item: projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "media", Name: "heat"}, Kind: commonv1.MediaKindMovie, Tab: projection.TabMovies, Title: "Heat",
	}, CanScan: true}
	require.Equal(t, []views.Action{views.ActionRefresh, views.ActionSearch, views.ActionInteractive, views.ActionRename, views.ActionEdit, views.ActionImages, views.ActionDelete}, views.DefaultActions(movie))
	require.Contains(t, render(t, views.ItemActions(movie, views.DefaultActions(movie))), `data-action="movie-interactive-search"`)
	for _, kind := range []commonv1.MediaKind{commonv1.MediaKindSeries, commonv1.MediaKindArtist, commonv1.MediaKindAuthor, commonv1.MediaKindComic} {
		require.False(t, views.InteractiveSearchable(kind), kind)
		container := movie
		container.Item.Kind = kind
		require.NotContains(t, render(t, views.ItemActions(container, views.DefaultActions(container))), `-interactive-search"`, "%s: the interactive search is its children's", kind)
	}
	for _, kind := range []commonv1.MediaKind{commonv1.MediaKindMovie, commonv1.MediaKindEpisode, commonv1.MediaKindAlbum, commonv1.MediaKindBook, commonv1.MediaKindIssue, commonv1.MediaKindAudiobook} {
		require.True(t, views.InteractiveSearchable(kind), kind)
	}
}
