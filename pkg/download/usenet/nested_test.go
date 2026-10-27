/*
Copyright 2026 The clustarr Authors.

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
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// zipOf is a zip holding entries, in the order given.
func zipOf(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// Go writes zips and not rars, and the nesting is the same for every
// archive kind the unpacker opens, so these nest zips.
func TestUnpackArchivesOpensAnArchiveInsideAnArchive(t *testing.T) {
	// A rar posted inside a rar used to be published as an archive, which
	// the import then refused as the release's fault. The inner archive is
	// extracted into the directory it sits in and removed.
	dir := t.TempDir()
	episode := make([]byte, 64<<10)
	_, _ = rand.New(rand.NewSource(3)).Read(episode) //nolint:gosec // a fixture
	inner := zipOf(t, map[string][]byte{"Show.S01E01.1080p.mkv": episode})
	outer := zipOf(t, map[string][]byte{"Season 1/inner.zip": inner, "readme.nfo": []byte("hi")})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "outer.zip"), outer, 0o644))

	dst := filepath.Join(dir, "out")
	res, err := unpackArchives(context.Background(), dir, dst, "", []nzbFile{{Name: "outer.zip", Kind: kindArchive}}, nil)
	require.NoError(t, err)
	require.False(t, res.Encrypted)

	got, err := os.ReadFile(filepath.Join(dst, "Season 1", "Show.S01E01.1080p.mkv"))
	require.NoError(t, err, "the nested archive's file lands beside where the archive was")
	require.True(t, bytes.Equal(episode, got))
	require.NoFileExists(t, filepath.Join(dst, "Season 1", "inner.zip"), "the extracted inner archive goes")
	require.FileExists(t, filepath.Join(dst, "readme.nfo"))
}

func TestUnpackArchivesStopsAtTheNestingLimit(t *testing.T) {
	// outer > a > b > c > d > deep.txt: the unpack opens a, b and c
	// (maxNestedUnpack levels) and leaves d as it is, rather than follow an
	// archive that nests without end.
	dir := t.TempDir()
	d := zipOf(t, map[string][]byte{"deep.txt": []byte("deep")})
	c := zipOf(t, map[string][]byte{"d.zip": d})
	b := zipOf(t, map[string][]byte{"c.zip": c})
	a := zipOf(t, map[string][]byte{"b.zip": b})
	outer := zipOf(t, map[string][]byte{"a.zip": a})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "outer.zip"), outer, 0o644))

	dst := filepath.Join(dir, "out")
	_, err := unpackArchives(context.Background(), dir, dst, "", []nzbFile{{Name: "outer.zip", Kind: kindArchive}}, nil)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(dst, "d.zip"), "the archive past the limit is left")
	require.NoFileExists(t, filepath.Join(dst, "deep.txt"))
	for _, gone := range []string{"a.zip", "b.zip", "c.zip"} {
		require.NoFileExists(t, filepath.Join(dst, gone), "%s was opened and removed", gone)
	}
}

func TestAnEncryptedNestedArchiveIsReported(t *testing.T) {
	dir := t.TempDir()
	outer := zipOf(t, map[string][]byte{"locked.zip": encryptedZip(t)})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "outer.zip"), outer, 0o644))

	res, err := unpackArchives(context.Background(), dir, filepath.Join(dir, "out"), "", []nzbFile{{Name: "outer.zip", Kind: kindArchive}}, nil)
	require.ErrorIs(t, err, ErrEncrypted)
	require.True(t, res.Encrypted)
}

func TestArchiveSetFilesNamesEveryVolumeOfTheSet(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"inner.part01.rar", "inner.part02.rar", "inner.sfv", "other.rar", "old.rar", "old.r00", "old.r01", "Inner.part03.rar"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644))
	}
	require.ElementsMatch(t, []string{"inner.part01.rar", "inner.part02.rar", "Inner.part03.rar"}, archiveSetFiles(dir, "inner.part01.rar"))
	require.ElementsMatch(t, []string{"old.rar", "old.r00", "old.r01"}, archiveSetFiles(dir, "old.rar"))
	require.ElementsMatch(t, []string{"other.rar"}, archiveSetFiles(dir, "other.rar"))
	require.Nil(t, archiveSetFiles(dir, "inner.sfv"), "no archive, no set")
}
