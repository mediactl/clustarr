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

package usenet

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// seasonOfBareFiles is a post of two bare episodes and no par2, the first of
// articles parts of size bytes each; missing names the articles of the
// second file no server holds.
func seasonOfBareFiles(t *testing.T, srv *stubServer, title string, articles, size int, missing ...int) ([]byte, [][]byte) {
	t.Helper()
	files := make([]fileSpec, 2)
	whole := make([][]byte, 2)
	for f := range files {
		parts := make([][]byte, articles)
		for i := range parts {
			parts[i] = partPayload(byte(f*31+i), size)
			whole[f] = append(whole[f], parts[i]...)
		}
		files[f] = fileSpec{name: fmt.Sprintf("Show.S01E%02d.mkv", f+1), parts: parts}
	}
	nzb := buildNZB(t, srv, title, files)
	for _, i := range missing {
		srv.refuse[fmt.Sprintf("f1-p%d@clustarr.test", i)] = 430
	}
	return nzb, whole
}

// A season of bare files one article short of two thousand is assembled and
// published: the file with the hole is its declared size with the hole
// where the article was, and the other is whole. On talos (2026-11-27) a
// 76 GB season of 49 episodes was failed and blocked for one article of
// 103,312, every server and the backup asked.
func TestBareContentAFewArticlesShortIsPublishedWithItsHoles(t *testing.T) {
	const articles, size = 1000, 64
	srv := newStubServer(t)
	nzb, whole := seasonOfBareFiles(t, srv, "Season", articles, size, 500)

	c, _, dataDir := newTestClient(t, Config{
		Providers:    []Provider{srv.provider("solo", 4, 1)},
		HealthAction: downloadv1alpha1.HealthActionDelete,
	})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Season", Payload: nzb, Category: "tv"})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)

	first, err := os.ReadFile(filepath.Join(dataDir, "tv", "Season", "Show.S01E01.mkv"))
	require.NoError(t, err)
	require.Equal(t, whole[0], first, "the file nothing is missing from")
	second, err := os.ReadFile(filepath.Join(dataDir, "tv", "Season", "Show.S01E02.mkv"))
	require.NoError(t, err)
	require.Len(t, second, len(whole[1]), "the holed file keeps its declared size")
	require.Equal(t, whole[1][:500*size], second[:500*size])
	require.Equal(t, make([]byte, size), second[500*size:501*size], "the missing article is a hole")
	require.Equal(t, whole[1][501*size:], second[501*size:])
}

// More than a handful, or more than one article in a thousand, is still the
// release's fault: three of two thousand are few and too large a share.
func TestBareContentShortByMoreThanAFewIsNotPublished(t *testing.T) {
	for name, tc := range map[string]struct {
		articles int
		missing  []int
	}{
		"too large a share": {1000, []int{10, 500, 900}},
		"too many":          {4000, []int{1, 2, 3, 4, 5, 6}},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newStubServer(t)
			nzb, _ := seasonOfBareFiles(t, srv, "Season", tc.articles, 16, tc.missing...)
			c, _, dataDir := newTestClient(t, Config{
				Providers:    []Provider{srv.provider("solo", 4, 1)},
				HealthAction: downloadv1alpha1.HealthActionDelete,
			})
			id, err := c.Add(context.Background(), download.AddRequest{Name: "Season", Payload: nzb, Category: "tv"})
			require.NoError(t, err)
			it := waitForTerminal(t, c, id)
			require.Equal(t, download.StatusFailed, it.Status, "message: %s", it.Message)
			require.Equal(t, commonv1alpha1.DownloadFailureMissingArticles, it.FailureReason)
			_, err = os.Stat(filepath.Join(dataDir, "tv", "Season"))
			require.True(t, os.IsNotExist(err), "nothing is published")
		})
	}
}

// An archive of a download a few articles short is opened without its
// checksums: the entry is written whole, hole and all. Without the mark the
// same archive fails, as an archive that should be whole must.
func TestAnArchiveWithAHoleIsUnpackedWhenTheDownloadIsAFewArticlesShort(t *testing.T) {
	body := bytes.Repeat([]byte("episode "), 4096)
	var z bytes.Buffer
	w := zip.NewWriter(&z)
	f, err := w.CreateHeader(&zip.FileHeader{Name: "Show.S01E01.mkv", Method: zip.Store})
	require.NoError(t, err)
	_, err = f.Write(body)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	raw := z.Bytes()
	at := bytes.Index(raw, body) + len(body)/2
	hole := make([]byte, 512)
	copy(raw[at:], hole)
	want := append([]byte(nil), body...)
	copy(want[len(body)/2:], hole)

	files := []nzbFile{{Name: "show.zip", Kind: kindArchive}}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "show.zip"), raw, 0o644))

	_, err = unpackArchives(context.Background(), dir, filepath.Join(dir, "strict"), "", files, nil)
	require.ErrorIs(t, err, zip.ErrChecksum)

	res, err := unpackArchives(withHoles(context.Background()), dir, filepath.Join(dir, "out"), "", files, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.Extracted)
	got, err := os.ReadFile(filepath.Join(dir, "out", "Show.S01E01.mkv"))
	require.NoError(t, err)
	require.Equal(t, want, got)
}
