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
	"bytes"
	"context"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// An obfuscated post names its files one way on the wire and another in the
// par2 set. Before repair the job gives each file the set's name, matched by
// the MD5 of its first 16 KiB, so par2 and the unpacker find what they
// expect without copying anything.
func TestRenameObfuscatedRestoresNamesFromThePar2Set(t *testing.T) {
	bin := par2Binary(t)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{{Name: "p", Host: "127.0.0.1", Port: 1, Connections: 1}}})

	// Build a set with real par2, then obfuscate the data file's name.
	work := t.TempDir()
	data := make([]byte, 200<<10)
	_, _ = rand.New(rand.NewSource(7)).Read(data)
	require.NoError(t, os.WriteFile(filepath.Join(work, "Some.Movie.2026.1080p.mkv"), data, 0o644))
	create := exec.Command(bin, "c", "-q", "-b16", "-r10", "--", "Some.Movie.2026.1080p.par2", "Some.Movie.2026.1080p.mkv")
	create.Dir = work
	out, err := create.CombinedOutput()
	require.NoErrorf(t, err, "par2 create: %s", out)

	descs := func() []par2FileDesc {
		f, err := os.Open(filepath.Join(work, "Some.Movie.2026.1080p.par2"))
		require.NoError(t, err)
		defer func() { _ = f.Close() }()
		d, err := parsePar2FileDescs(f)
		require.NoError(t, err)
		return d
	}()
	require.Len(t, descs, 1)
	require.Equal(t, "Some.Movie.2026.1080p.mkv", descs[0].Name)
	h, err := hash16k(filepath.Join(work, "Some.Movie.2026.1080p.mkv"))
	require.NoError(t, err)
	require.Equal(t, h, descs[0].Hash16k, "the FileDesc hash16k is the MD5 of the first 16 KiB")

	// The job saw the wire names: an obfuscated data file, a par2 index
	// with a bare name, and one volume.
	nzb := nzbWith(t, "", "z75QORTuwk9NHlDJbb53izyP7TQqFzG4", "0a1b2c3d", "Some.Movie.2026.1080p.vol00+1.par2")
	parsed, err := parseNZB(nzb, defaultMaxNZBBytes)
	require.NoError(t, err)
	j := c.newJob("job1", "download-1", "movies", filepath.Join(c.cfg.ScratchDir, "movies", "job1"), parsed, nzb)
	require.NoError(t, os.MkdirAll(j.contentDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(j.dir, nzbName), nzb, 0o644))
	require.NoError(t, os.Rename(filepath.Join(work, "Some.Movie.2026.1080p.mkv"), filepath.Join(j.contentDir(), "z75QORTuwk9NHlDJbb53izyP7TQqFzG4")))
	require.NoError(t, os.Rename(filepath.Join(work, "Some.Movie.2026.1080p.par2"), filepath.Join(j.contentDir(), "0a1b2c3d")))
	vols, _ := filepath.Glob(filepath.Join(work, "*.vol*.par2"))
	require.NotEmpty(t, vols)
	require.NoError(t, os.Rename(vols[0], filepath.Join(j.contentDir(), "Some.Movie.2026.1080p.vol00+1.par2")))

	j.renameObfuscated(context.Background())

	_, err = os.Stat(filepath.Join(j.contentDir(), "Some.Movie.2026.1080p.mkv"))
	require.NoError(t, err, "the data file carries the set's name")
	_, err = os.Stat(filepath.Join(j.contentDir(), "0a1b2c3d.par2"))
	require.NoError(t, err, "a bare par2 index gets its extension from its magic bytes")
	names := map[string]fileKind{}
	for _, f := range j.nzb.Files {
		names[f.Name] = f.Kind
	}
	require.Equal(t, kindContent, names["Some.Movie.2026.1080p.mkv"])
	require.Equal(t, kindPar2Index, names["0a1b2c3d.par2"])
	require.Equal(t, "Some.Movie.2026.1080p.mkv", j.renames["z75QORTuwk9NHlDJbb53izyP7TQqFzG4"])

	// A restart re-parses the wire names and replays the renames.
	reparsed, err := parseNZB(nzb, defaultMaxNZBBytes)
	require.NoError(t, err)
	applyRenames(reparsed.Files, j.renames)
	require.Equal(t, "Some.Movie.2026.1080p.mkv", reparsed.Files[0].Name)
	require.Equal(t, "Some.Movie.2026.1080p.mkv", par2IndexFileOrContent(reparsed.Files))
}

func par2IndexFileOrContent(files []nzbFile) string {
	for _, f := range files {
		if f.Kind == kindContent {
			return f.Name
		}
	}
	return ""
}

func TestSniffExtensionKnowsTheContainersThatMatter(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]struct {
		head []byte
		want string
	}{
		"ebml":  {[]byte{0x1a, 0x45, 0xdf, 0xa3, 0, 0, 0, 0, 0, 0, 0, 0}, ".mkv"},
		"rar":   {[]byte("Rar!\x1a\x07\x01\x00xxxxxxxx"), ".rar"},
		"par2":  {append(append([]byte{}, par2Magic...), 0, 0, 0, 0, 0, 0, 0, 0), ".par2"},
		"mp4":   {[]byte("\x00\x00\x00\x20ftypisom\x00\x00\x02\x00"), ".mp4"},
		"avi":   {[]byte("RIFF\x00\x00\x00\x00AVI LIST"), ".avi"},
		"7z":    {[]byte{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c, 0, 4, 0, 0, 0, 0}, ".7z"},
		"zip":   {[]byte("PK\x03\x04\x14\x00\x00\x00\x08\x00\x00\x00"), ".zip"},
		"text":  {[]byte("just some words here"), ""},
		"short": {[]byte("PK"), ""},
	}
	for name, tc := range cases {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, tc.head, 0o644))
		require.Equal(t, tc.want, sniffExtension(p), name)
	}
	require.True(t, hasUsableExtension("movie.mkv"))
	require.True(t, hasUsableExtension("set.vol000+01.par2"))
	require.True(t, hasUsableExtension("release.r03"))
	require.False(t, hasUsableExtension("z75QORTuwk9NHlDJbb53izyP7TQqFzG4"))
	require.False(t, hasUsableExtension("weird.bin"))
}

func TestFailedInsideArchiveLooksOnlyAtArchiveVolumes(t *testing.T) {
	c, _, _ := newTestClient(t, Config{Providers: []Provider{{Name: "p", Host: "127.0.0.1", Port: 1, Connections: 1}}})
	nzb := nzbWith(t, "", "release.part01.rar", "release.nfo", "release.par2")
	parsed, err := parseNZB(nzb, defaultMaxNZBBytes)
	require.NoError(t, err)
	j := c.newJob("job2", "download-2", "movies", filepath.Join(c.cfg.ScratchDir, "movies", "job2"), parsed, nzb)
	require.False(t, j.failedInsideArchive(), "nothing missing")
	j.failedSegs[1].set(0) // the nfo
	require.False(t, j.failedInsideArchive(), "a hole in the nfo leaves the archive whole")
	j.failedSegs[0].set(0) // the rar
	require.True(t, j.failedInsideArchive())
}

// A file missing its first articles hashes to nothing the set knows, since
// hash16k covers exactly the bytes that are gone. The IFSC packets know
// every slice of it, and any intact slice names it -- which is what stops
// par2 from rebuilding it under the set's name beside the holed copy.
func TestRenameObfuscatedMatchesADamagedFileByAnIntactSlice(t *testing.T) {
	bin := par2Binary(t)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{{Name: "p", Host: "127.0.0.1", Port: 1, Connections: 1}}, Par2Path: bin})

	work := t.TempDir()
	data := make([]byte, 200<<10)
	_, _ = rand.New(rand.NewSource(11)).Read(data)
	require.NoError(t, os.WriteFile(filepath.Join(work, "Some.Movie.2026.1080p.mkv"), data, 0o644))
	create := exec.Command(bin, "c", "-q", "-b16", "-r50", "--", "Some.Movie.2026.1080p.par2", "Some.Movie.2026.1080p.mkv")
	create.Dir = work
	out, err := create.CombinedOutput()
	require.NoErrorf(t, err, "par2 create: %s", out)

	nzb := nzbWith(t, "", "z75QORTuwk9NHlDJbb53izyP7TQqFzG4", "Some.Movie.2026.1080p.par2", "Some.Movie.2026.1080p.vol00+8.par2")
	parsed, err := parseNZB(nzb, defaultMaxNZBBytes)
	require.NoError(t, err)
	j := c.newJob("job1", "download-1", "movies", filepath.Join(c.cfg.ScratchDir, "movies", "job1"), parsed, nzb)
	require.NoError(t, os.MkdirAll(j.contentDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(j.dir, nzbName), nzb, 0o644))
	// The wire copy has a hole across its first 4 KiB: inside hash16k's
	// window and inside slice 0.
	holed := append([]byte{}, data...)
	for i := range holed[:4<<10] {
		holed[i] = 0
	}
	require.NoError(t, os.WriteFile(filepath.Join(j.contentDir(), "z75QORTuwk9NHlDJbb53izyP7TQqFzG4"), holed, 0o644))
	require.NoError(t, os.Rename(filepath.Join(work, "Some.Movie.2026.1080p.par2"), filepath.Join(j.contentDir(), "Some.Movie.2026.1080p.par2")))
	vols, _ := filepath.Glob(filepath.Join(work, "*.vol*.par2"))
	require.NotEmpty(t, vols)
	require.NoError(t, os.Rename(vols[0], filepath.Join(j.contentDir(), "Some.Movie.2026.1080p.vol00+8.par2")))
	j.failedSegs[0].set(0)

	j.renameObfuscated(context.Background())

	_, err = os.Stat(filepath.Join(j.contentDir(), "Some.Movie.2026.1080p.mkv"))
	require.NoError(t, err, "the holed file carries the set's name, matched by an intact slice")
	require.Equal(t, "Some.Movie.2026.1080p.mkv", j.renames["z75QORTuwk9NHlDJbb53izyP7TQqFzG4"])

	// par2 now repairs it in place rather than beside itself.
	require.NoError(t, j.repair(context.Background()))
	got, err := os.ReadFile(filepath.Join(j.contentDir(), "Some.Movie.2026.1080p.mkv"))
	require.NoError(t, err)
	require.True(t, bytes.Equal(data, got), "the file under the set's name is the repaired one")
	entries, err := os.ReadDir(j.contentDir())
	require.NoError(t, err)
	for _, e := range entries {
		require.NotEqual(t, "z75QORTuwk9NHlDJbb53izyP7TQqFzG4", e.Name(), "no second copy under the wire name")
	}
}

// The 2026-09-24 grab: par2 rebuilt part01 under the set's obfuscated name
// (every article of it had failed, so nothing on disk could be matched)
// and left a holed part02 under its wire name; the unpacker, keyed on the
// wire names, started from the holed copy and could not follow the chain.
// After a repair the set's files are the content.
func TestAdoptRepairedSetTakesTheTargetsParRebuiltUnderTheSetsName(t *testing.T) {
	bin := par2Binary(t)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{{Name: "p", Host: "127.0.0.1", Port: 1, Connections: 1}}, Par2Path: bin})

	work := t.TempDir()
	rng := rand.New(rand.NewSource(13))
	part := func(name string) []byte {
		b := make([]byte, 100<<10)
		_, _ = rng.Read(b)
		require.NoError(t, os.WriteFile(filepath.Join(work, name), b, 0o644))
		return b
	}
	p1 := part("2ef6f194995e4a11b055d0f2354ef0ba.part01.rar")
	p2 := part("2ef6f194995e4a11b055d0f2354ef0ba.part02.rar")
	create := exec.Command(bin, "c", "-q", "-b16", "-r200", "-n1", "--", "set.par2",
		"2ef6f194995e4a11b055d0f2354ef0ba.part01.rar", "2ef6f194995e4a11b055d0f2354ef0ba.part02.rar")
	create.Dir = work
	out, err := create.CombinedOutput()
	require.NoErrorf(t, err, "par2 create: %s", out)

	nzb := nzbWith(t, "", "A.Scanner.Darkly.part01.rar", "A.Scanner.Darkly.part02.rar", "set.par2", "set.vol00+32.par2")
	parsed, err := parseNZB(nzb, defaultMaxNZBBytes)
	require.NoError(t, err)
	j := c.newJob("job1", "download-1", "movies", filepath.Join(c.cfg.ScratchDir, "movies", "job1"), parsed, nzb)
	require.NoError(t, os.MkdirAll(j.contentDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(j.dir, nzbName), nzb, 0o644))
	// part01 never reached the disk; part02 is there under its wire name
	// with every slice holed, so no checksum names it.
	j.failedSegs[0].set(0)
	j.failedSegs[1].set(0)
	require.NoError(t, os.WriteFile(filepath.Join(j.contentDir(), "A.Scanner.Darkly.part02.rar"), make([]byte, len(p2)), 0o644))
	require.NoError(t, os.Rename(filepath.Join(work, "set.par2"), filepath.Join(j.contentDir(), "set.par2")))
	vols, _ := filepath.Glob(filepath.Join(work, "*.vol*.par2"))
	require.NotEmpty(t, vols)
	require.NoError(t, os.Rename(vols[0], filepath.Join(j.contentDir(), "set.vol00+32.par2")))

	j.renameObfuscated(context.Background())
	require.NoError(t, j.repair(context.Background()))

	require.Equal(t, []string{"2ef6f194995e4a11b055d0f2354ef0ba.part01.rar"}, archiveEntryPoints(j.nzb.Files),
		"the entry point is the set's part01, not the wire name nothing was written under")
	kinds := map[string]fileKind{}
	for _, f := range j.nzb.Files {
		kinds[f.Name] = f.Kind
	}
	require.Equal(t, kindArchive, kinds["2ef6f194995e4a11b055d0f2354ef0ba.part02.rar"], "the rebuilt part02 is carried as a volume of the chain")
	require.Equal(t, kindContent, kinds["A.Scanner.Darkly.part02.rar"], "the holed copy is no volume of anything")
	for name, want := range map[string][]byte{
		"2ef6f194995e4a11b055d0f2354ef0ba.part01.rar": p1,
		"2ef6f194995e4a11b055d0f2354ef0ba.part02.rar": p2,
	} {
		got, err := os.ReadFile(filepath.Join(j.contentDir(), name))
		require.NoError(t, err, name)
		require.True(t, bytes.Equal(want, got), "%s is the set's file", name)
	}
	_, err = os.Stat(filepath.Join(j.contentDir(), "A.Scanner.Darkly.part02.rar"))
	require.ErrorIs(t, err, os.ErrNotExist, "the holed copy is gone")
}
