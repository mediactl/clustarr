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
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/test/par2test"
)

func TestIsProbablyObfuscated(t *testing.T) {
	for name, want := range map[string]bool{
		"02fb98288e35eea0f5b10ea420980959.mkv":                   true, // an MD5
		"z75QORTuwk9NHlDJbb53izyP7TQqFzG4.mkv":                   true, // a random token, no separator
		"0675e29e9abfd2.f7d069dab0b853283cc1b069a25f82.6547.mkv": true,
		"abc.xyz":                           true,
		"Show.Name.S01E03.1080p.WEB-DL.mkv": false,
		"Great Distro.iso":                  false,
		"Beast 2020.mkv":                    false,
		"Catullus.epub":                     false,
		"ubuntu-22.04-desktop-amd64.iso":    false,
		"S01E02 - To Affection.mkv":         false,
	} {
		require.Equal(t, want, isProbablyObfuscated(name), name)
	}
}

// hashedJob is a job whose NZB names the given files, with a content
// directory on disk and the given publish name.
func hashedJob(t *testing.T, name string, files ...string) *job {
	t.Helper()
	c, _, _ := newTestClient(t, Config{Providers: []Provider{{Name: "p", Host: "127.0.0.1", Port: 1, Connections: 1}}})
	nzb := nzbWith(t, "", files...)
	parsed, err := parseNZB(nzb, defaultMaxNZBBytes)
	require.NoError(t, err)
	j := c.newJob("job1", name, "tv", filepath.Join(c.cfg.ScratchDir, "tv", "job1"), parsed)
	require.NoError(t, os.MkdirAll(j.contentDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(j.dir, nzbName), nzb, 0o644))
	return j
}

// bigFile writes a sparse file of size bytes at path, head first.
func bigFile(t *testing.T, path string, size int64, head []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path)
	require.NoError(t, err)
	_, err = f.Write(head)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())
}

func TestAnInnerPar2SetNamesTheUnpackedFiles(t *testing.T) {
	// An obfuscated season pack: the archive unpacks ten hashed .mkv files
	// beside the par2 set the uploader built against their real names
	// (SABnzbd's process_unpacked_par2). The set names them; the par2
	// files, spent, go.
	j := hashedJob(t, "Show.S01.1080p.WEB-DL", "Show.S01.part01.rar")
	unpacked := filepath.Join(j.dir, unpackDirName)
	require.NoError(t, os.MkdirAll(unpacked, 0o755))
	rng := rand.New(rand.NewSource(7)) //nolint:gosec // a fixture
	for _, n := range []string{"Show.S01E01.1080p.WEB-DL.mkv", "Show.S01E02.1080p.WEB-DL.mkv"} {
		data := make([]byte, 200<<10)
		_, _ = rng.Read(data)
		require.NoError(t, os.WriteFile(filepath.Join(unpacked, n), data, 0o644))
	}
	par2test.CreateSet(t, unpacked, "Show.S01.1080p.WEB-DL.par2",
		[]string{"Show.S01E01.1080p.WEB-DL.mkv", "Show.S01E02.1080p.WEB-DL.mkv"}, 16, 10, 0)
	require.NoError(t, os.Rename(filepath.Join(unpacked, "Show.S01E01.1080p.WEB-DL.mkv"),
		filepath.Join(unpacked, "02fb98288e35eea0f5b10ea420980959.mkv")))
	require.NoError(t, os.Rename(filepath.Join(unpacked, "Show.S01E02.1080p.WEB-DL.mkv"),
		filepath.Join(unpacked, "9c0a7f5b1d2e3a4b5c6d7e8f90123456.mkv")))

	j.finalNames(context.Background(), unpacked)

	require.FileExists(t, filepath.Join(unpacked, "Show.S01E01.1080p.WEB-DL.mkv"))
	require.FileExists(t, filepath.Join(unpacked, "Show.S01E02.1080p.WEB-DL.mkv"))
	require.NoFileExists(t, filepath.Join(unpacked, "02fb98288e35eea0f5b10ea420980959.mkv"))
	left, err := filepath.Glob(filepath.Join(unpacked, "*.par2"))
	require.NoError(t, err)
	require.Empty(t, left, "the inner par2 files are spent once they have named the files")
}

func TestALoneObfuscatedFileTakesTheJobName(t *testing.T) {
	// SABnzbd's "Deobfuscate final filenames": one big file whose name is
	// probably obfuscated is named for the job. A pack's hashed files keep
	// their names -- the job name cannot say which episode each is, and the
	// importer reads each file's container title instead.
	ebml := []byte{0x1a, 0x45, 0xdf, 0xa3}
	for name, tc := range map[string]struct {
		files []string
		head  []byte
		want  []string
	}{
		"one hashed file":   {[]string{"02fb98288e35eea0f5b10ea420980959.mkv"}, nil, []string{"Show.S01E01.1080p.WEB-DL.mkv"}},
		"no extension":      {[]string{"02fb98288e35eea0f5b10ea420980959"}, ebml, []string{"Show.S01E01.1080p.WEB-DL.mkv"}},
		"a pack's files":    {[]string{"02fb98288e35eea0f5b10ea420980959.mkv", "9c0a7f5b1d2e3a4b5c6d7e8f90123456.mkv"}, nil, []string{"02fb98288e35eea0f5b10ea420980959.mkv", "9c0a7f5b1d2e3a4b5c6d7e8f90123456.mkv"}},
		"a named file":      {[]string{"Other.Name.S01E01.mkv"}, nil, []string{"Other.Name.S01E01.mkv"}},
		"a hashed sample":   {[]string{"02fb98288e35eea0f5b10ea420980959.mkv", "sample.mkv"}, nil, []string{"Show.S01E01.1080p.WEB-DL.mkv", "sample.mkv"}},
		"a disc structure":  {[]string{"00001.m2ts"}, nil, []string{"00001.m2ts"}},
		"a hashed rar left": {[]string{"02fb98288e35eea0f5b10ea420980959.rar"}, nil, []string{"02fb98288e35eea0f5b10ea420980959.rar"}},
	} {
		t.Run(name, func(t *testing.T) {
			j := hashedJob(t, "Show.S01E01.1080p.WEB-DL", "Show.S01E01.part01.rar")
			unpacked := filepath.Join(j.dir, unpackDirName)
			for _, f := range tc.files {
				size := int64(obfuscatedMinBytes + 1)
				if f == "sample.mkv" {
					size = 1 << 20
				}
				bigFile(t, filepath.Join(unpacked, f), size, tc.head)
			}

			j.finalNames(context.Background(), unpacked)

			entries, err := os.ReadDir(unpacked)
			require.NoError(t, err)
			var got []string
			for _, e := range entries {
				got = append(got, e.Name())
			}
			require.ElementsMatch(t, tc.want, got)
		})
	}
}

func TestFinalNamesOnTheContentKeepTheJobsBooks(t *testing.T) {
	// Nothing was unpacked: the downloaded file is the content, and its new
	// name must reach the job's file list and the manifest, as every rename
	// of a content file does (renameObfuscated), so a restart sees it.
	j := hashedJob(t, "Show.S01E01.1080p.WEB-DL", "02fb98288e35eea0f5b10ea420980959.mkv")
	bigFile(t, filepath.Join(j.contentDir(), "02fb98288e35eea0f5b10ea420980959.mkv"), obfuscatedMinBytes+1, nil)

	j.finalNames(context.Background(), j.contentDir())

	require.FileExists(t, filepath.Join(j.contentDir(), "Show.S01E01.1080p.WEB-DL.mkv"))
	require.Equal(t, "Show.S01E01.1080p.WEB-DL.mkv", j.nzb.Files[0].Name)
	require.Equal(t, "Show.S01E01.1080p.WEB-DL.mkv", j.renames["02fb98288e35eea0f5b10ea420980959.mkv"])
}
