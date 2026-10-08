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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/par2child"
	"github.com/mediactl/clustarr/test/par2test"
)

// libraryRepairer is the repairer the real-par2 tests use: par2go
// in-process. A box where the library cannot load (PAR2GO_LIB) skips, or
// fails under PAR2GO_REQUIRE=1 (make test).
func libraryRepairer(t *testing.T) *LibraryRepairer {
	t.Helper()
	par2test.RequireLibrary(t)
	return &LibraryRepairer{Runner: par2child.InProcess{}}
}

func TestLibraryRepairerVerifiesAnIntactSet(t *testing.T) {
	r := libraryRepairer(t)
	dir := t.TempDir()

	data := make([]byte, 128<<10)
	rnd := rand.New(rand.NewSource(1))
	_, _ = rnd.Read(data)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "movie.mkv"), data, 0o644))

	// -b caps the block count: par2cmdline's default of 2000 blocks over a
	// small file makes the Reed-Solomon matrix enormous and the test minutes
	// long, which is a property of the tool and not of anything under test.
	par2test.CreateSet(t, dir, "movie.mkv.par2", []string{"movie.mkv"}, 16, 20, 0)

	res, err := r.Repair(context.Background(), dir, "movie.mkv.par2")
	require.NoError(t, err)
	require.True(t, res.AllCorrect, "an intact set must verify without repair: %s", res.Output)
	require.False(t, res.Repaired)
}

func TestLibraryRepairerRepairsADamagedFile(t *testing.T) {
	r := libraryRepairer(t)
	dir := t.TempDir()

	data := make([]byte, 128<<10)
	rnd := rand.New(rand.NewSource(2))
	_, _ = rnd.Read(data)
	target := filepath.Join(dir, "movie.mkv")
	require.NoError(t, os.WriteFile(target, data, 0o644))

	par2test.CreateSet(t, dir, "movie.mkv.par2", []string{"movie.mkv"}, 16, 50, 0)

	// Corrupt a slice in the middle.
	damaged := append([]byte(nil), data...)
	for i := 40 << 10; i < 44<<10; i++ {
		damaged[i] ^= 0xFF
	}
	require.NoError(t, os.WriteFile(target, damaged, 0o644))

	res, err := r.Repair(context.Background(), dir, "movie.mkv.par2")
	require.NoError(t, err, "output: %s", res.Output)
	require.True(t, res.Repaired, "output: %s", res.Output)

	fixed, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, data, fixed, "the repaired file must match the original byte for byte")
}

// TestLibraryRepairerRepairsASetWhoseFilesCarryOtherNames pins the first
// real grab on the owner's cluster (2026-09-24): the par2 set described
// obfuscated names while the files on disk carried the NZB subjects' names,
// and "par2 r index" with no extra files reported every target missing after
// a complete 10 GB transfer. Given the directory's files as extras, par2
// matches them by content and restores the recorded names.
func TestLibraryRepairerRepairsASetWhoseFilesCarryOtherNames(t *testing.T) {
	r := libraryRepairer(t)
	dir := t.TempDir()

	data := make([]byte, 128<<10)
	rnd := rand.New(rand.NewSource(3))
	_, _ = rnd.Read(data)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "movie.mkv"), data, 0o644))

	par2test.CreateSet(t, dir, "movie.mkv.par2", []string{"movie.mkv"}, 16, 20, 0)

	// The poster's subject named it differently from the par2 set.
	require.NoError(t, os.Rename(filepath.Join(dir, "movie.mkv"),
		filepath.Join(dir, "13th.2016.1080p.WEBRip.X264-DEFLATE.part01.rar")))

	res, err := r.Repair(context.Background(), dir, "movie.mkv.par2")
	require.NoError(t, err, "output: %s", res.Output)
	require.True(t, res.Repaired || res.AllCorrect, "output: %s", res.Output)

	fixed, err := os.ReadFile(filepath.Join(dir, "movie.mkv"))
	require.NoError(t, err, "the target must exist under the name the set records")
	require.Equal(t, data, fixed)
}

func TestExtraFilesListsRegularFilesButNotTheIndex(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"b.rar", "a.rar", "set.par2", "set.vol-01.par2"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644))
	}
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	require.Equal(t, []string{"a.rar", "b.rar", "set.vol-01.par2"}, extraFiles(dir, "set.par2"))
	require.Nil(t, extraFiles(filepath.Join(dir, "missing"), "set.par2"))
}

func TestTailTruncatesFromTheFront(t *testing.T) {
	require.Equal(t, "short", tail("  short  ", 100))
	require.Equal(t, "..."+strings.Repeat("z", 5), tail(strings.Repeat("y", 3)+strings.Repeat("z", 5), 5))
}
