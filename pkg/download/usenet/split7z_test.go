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
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/test/par2test"
)

// A 7-Zip archive posted in parts, "x.7z.001" and on, is an archive: it
// keeps its names, its first part is the set's entry point, and it is
// unpacked through every part. Read as content, the first part's 7z magic
// earned it a second ".7z"; par2, whose set records "x.7z.001", put the
// name back; and the job failed as "the transfer's scratch files are gone
// or altered: stat .../GiDY8v0wuYowRRaQ.7z.001.7z" (talos, 2026-11-28, a
// film; the failure had been open since October).
func TestA7zArchiveInPartsKeepsItsNamesAndIsUnpacked(t *testing.T) {
	for _, name := range []string{"GiDY8v0wuYowRRaQ.7z.001", "GiDY8v0wuYowRRaQ.7z.002", "Show.7Z.014"} {
		require.Equal(t, kindArchive, classify(name), name)
		require.True(t, hasUsableExtension(name), "%s is given no extension from its magic bytes", name)
	}
	require.Equal(t, kindContent, classify("notes.001"), "a bare numbered file is not one")

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "test", "data", "usenet", "show.7z"))
	require.NoError(t, err)
	dir := t.TempDir()
	cut := len(raw) / 3
	parts := map[string][]byte{"show.7z.001": raw[:cut], "show.7z.002": raw[cut : 2*cut], "show.7z.003": raw[2*cut:]}
	var files []nzbFile
	for _, name := range []string{"show.7z.003", "show.7z.001", "show.7z.002"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), parts[name], 0o644))
		files = append(files, nzbFile{Name: name, Kind: classify(name), Bytes: int64(len(parts[name]))})
	}
	require.Equal(t, []string{"show.7z.001"}, archiveEntryPoints(files), "one set, entered by its first part")
	require.ElementsMatch(t, []string{"show.7z.001", "show.7z.002", "show.7z.003"}, archiveSetFiles(dir, "show.7z.001"))

	out := filepath.Join(dir, "out")
	res, err := unpackArchives(context.Background(), dir, out, "", files, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.Extracted)
	got, err := os.ReadFile(filepath.Join(out, "Show.S01E01.mkv"))
	require.NoError(t, err)
	want := make([]byte, 6000)
	for i := range want {
		want[i] = byte((i*7 + 3) % 251)
	}
	require.Equal(t, want, got)
}

// A file the par2 set records by name keeps that name whatever its bytes
// begin with: an extension from its magic bytes would take it from under
// par2, which looks for the set's names.
func TestAFileThePar2SetNamesIsGivenNoExtension(t *testing.T) {
	c, _, _ := newTestClient(t, Config{Providers: []Provider{{Name: "p", Host: "127.0.0.1", Port: 1, Connections: 1}}})
	work := t.TempDir()
	data := make([]byte, 200<<10)
	_, _ = rand.New(rand.NewSource(9)).Read(data)
	copy(data, []byte{0x1a, 0x45, 0xdf, 0xa3}) // EBML: sniffed, it is ".mkv"
	require.NoError(t, os.WriteFile(filepath.Join(work, "payload.001"), data, 0o644))
	par2test.CreateSet(t, work, "payload.par2", []string{"payload.001"}, 16, 10, 0)

	nzb := nzbWith(t, "", "payload.001", "payload.par2")
	parsed, err := parseNZB(nzb, defaultMaxNZBBytes)
	require.NoError(t, err)
	j := c.newJob("job1", "download-1", "movies", filepath.Join(c.cfg.ScratchDir, "movies", "job1"), parsed)
	require.NoError(t, os.MkdirAll(j.contentDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(j.dir, nzbName), nzb, 0o644))
	for _, name := range []string{"payload.001", "payload.par2"} {
		require.NoError(t, os.Rename(filepath.Join(work, name), filepath.Join(j.contentDir(), name)))
	}

	j.renameObfuscated(context.Background())

	_, err = os.Stat(filepath.Join(j.contentDir(), "payload.001"))
	require.NoError(t, err, "the file is where its par2 set names it")
	for _, f := range j.nzb.Files {
		require.NotEqual(t, "payload.001.mkv", f.Name)
	}
}
