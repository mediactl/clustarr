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

package mediainfo

import (
	"archive/tar"
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeCBZ(t *testing.T, path string, names ...string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	for _, n := range names {
		e, err := w.Create(n)
		require.NoError(t, err)
		_, _ = e.Write([]byte("x"))
	}
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())
}

func writeCBT(t *testing.T, path string, names ...string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	w := tar.NewWriter(f)
	for _, n := range names {
		require.NoError(t, w.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}))
		_, _ = w.Write([]byte("x"))
	}
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())
}

// A comic archive's pages are its image entries, listed and never
// extracted: a ComicInfo.xml, a folder and macOS's resource forks do not
// count.
func TestPageCountOfComicArchives(t *testing.T) {
	dir := t.TempDir()
	cbz := filepath.Join(dir, "Saga 001 (2012).cbz")
	writeCBZ(t, cbz, "Saga 001/001.jpg", "Saga 001/002.JPG", "Saga 001/003.png", "Saga 001/ComicInfo.xml", "__MACOSX/Saga 001/._001.jpg", "Saga 001/.hidden.jpg")
	n, err := PageCount(cbz)
	require.NoError(t, err)
	assert.EqualValues(t, 3, n)

	cbt := filepath.Join(dir, "issue.cbt")
	writeCBT(t, cbt, "01.webp", "02.webp", "notes.txt")
	n, err = PageCount(cbt)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
}

// A PDF's pages are its page objects; the page tree node is not one, and a
// token split across the scan's chunks is still counted once.
func TestPageCountOfAPDF(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "comic.pdf")
	var b strings.Builder
	b.WriteString("%PDF-1.4\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n2 0 obj << /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >> endobj\n")
	// Pad past the first scan chunk so the second page object straddles
	// the chunk boundary.
	b.WriteString("3 0 obj << /Type /Page /Parent 2 0 R >> endobj\n")
	pad := (1 << 20) - b.Len() - 8
	b.WriteString(strings.Repeat("%", pad))
	b.WriteString("4 0 obj << /Type /Page /Parent 2 0 R >> endobj\n%%EOF\n")
	require.NoError(t, os.WriteFile(pdf, []byte(b.String()), 0o644))
	n, err := PageCount(pdf)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
}

func TestPageCountRefusesOtherFiles(t *testing.T) {
	_, err := PageCount("movie.mkv")
	assert.ErrorIs(t, err, ErrNoPageCount)
	_, err = PageCount(filepath.Join(t.TempDir(), "missing.cbz"))
	assert.Error(t, err)
}

// The committed fixtures (2026-11-10 design D9, W3.1 step 2): a CBZ of two
// PNG pages beside its ComicInfo.xml, and a two-page PDF.
func TestPageCountOfACBZ(t *testing.T) {
	n, err := PageCount("../../test/data/mediainfo/comic_two_pages.cbz")
	require.NoError(t, err)
	assert.EqualValues(t, 2, n, "two images; ComicInfo.xml is no page")

	n, err = PageCount("../../test/data/mediainfo/book_two_pages.pdf")
	require.NoError(t, err)
	assert.EqualValues(t, 2, n, "two page objects; the page tree node is no page")
}
