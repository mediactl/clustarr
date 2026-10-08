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
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	par2 "github.com/mediactl/par2go"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/par2child"
)

// createSet runs `par2 c -q -b<blocks> -r<pct> [-n<files>] -- <index> <files>`
// in dir: par2go has no creator by design, so test sets come from the par2
// CLI (spec 2026-10-06 §8.8, OD26). It has test/par2test.CreateSet's
// signature, which replaces it once that package exists (the test batch's
// T2a). A box without the CLI skips the test.
func createSet(t testing.TB, dir, index string, files []string, blocks, redundancyPercent, recoveryFiles uint32) {
	t.Helper()
	bin, err := exec.LookPath("par2")
	if err != nil {
		t.Skip("the par2 CLI that makes test sets is not present on this box")
	}
	args := []string{"c", "-q", fmt.Sprintf("-b%d", blocks), fmt.Sprintf("-r%d", redundancyPercent)}
	if recoveryFiles > 0 {
		args = append(args, fmt.Sprintf("-n%d", recoveryFiles))
	}
	args = append(append(args, "--", index), files...)
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "par2 create: %s", out)
}

// libraryRepairer is the repairer the real-par2 tests use: par2go
// in-process. A box where the library cannot load (PAR2GO_LIB) skips, the
// contract the exec'd par2 binary's tests had.
func libraryRepairer(t *testing.T) *LibraryRepairer {
	t.Helper()
	if err := par2.Available(); err != nil {
		t.Skipf("par2go is unavailable on this box: %v", err)
	}
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
	createSet(t, dir, "movie.mkv.par2", []string{"movie.mkv"}, 16, 20, 0)

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

	createSet(t, dir, "movie.mkv.par2", []string{"movie.mkv"}, 16, 50, 0)

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

	createSet(t, dir, "movie.mkv.par2", []string{"movie.mkv"}, 16, 20, 0)

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
