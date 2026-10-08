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

package par2child_test

import (
	"bytes"
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	par2 "github.com/mediactl/par2go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/par2child"
	"github.com/mediactl/clustarr/test/par2test"
)

// set writes a 128 KiB movie.mkv into a new directory and makes a 16-block
// par2 set over it with pct per cent redundancy.
func set(t *testing.T, pct uint32) (dir string, data []byte) {
	t.Helper()
	dir = t.TempDir()
	data = make([]byte, 128<<10)
	_, _ = rand.New(rand.NewSource(1)).Read(data)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "movie.mkv"), data, 0o644))
	par2test.CreateSet(t, dir, "movie.mkv.par2", []string{"movie.mkv"}, 16, pct, 0)
	return dir, data
}

func repair(dir string, extras ...string) (par2child.Result, error) {
	return par2child.InProcess{}.Run(context.Background(), par2child.Request{
		Op: par2child.OpRepair, Index: filepath.Join(dir, "movie.mkv.par2"), Dir: dir, Extras: extras,
	})
}

func TestInProcessVerdicts(t *testing.T) {
	par2test.RequireLibrary(t)
	t.Run("intact", func(t *testing.T) {
		dir, _ := set(t, 20)
		res, err := repair(dir)
		require.NoError(t, err)
		assert.Equal(t, par2.AllCorrect, res.Status)
		assert.Equal(t, par2child.ErrKindNone, res.ErrKind)
		assert.Contains(t, res.Library, "libpar2shim", "the result names the library it ran")
	})
	t.Run("damaged", func(t *testing.T) {
		dir, data := set(t, 50)
		damaged := slices.Clone(data)
		copy(damaged[16<<10:], bytes.Repeat([]byte{0}, 4<<10))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "movie.mkv"), damaged, 0o644))
		res, err := repair(dir)
		require.NoError(t, err, "log: %s", res.Log)
		assert.Equal(t, par2.Repaired, res.Status)
		got, err := os.ReadFile(filepath.Join(dir, "movie.mkv"))
		require.NoError(t, err)
		assert.Equal(t, data, got, "the repaired file matches the original byte for byte")
	})
	t.Run("renamed", func(t *testing.T) {
		dir, data := set(t, 20)
		require.NoError(t, os.Rename(filepath.Join(dir, "movie.mkv"), filepath.Join(dir, "z75QO.part070.rar")))
		res, err := repair(dir, filepath.Join(dir, "z75QO.part070.rar"))
		require.NoError(t, err, "log: %s", res.Log)
		assert.Equal(t, par2.Repaired, res.Status)
		got, err := os.ReadFile(filepath.Join(dir, "movie.mkv"))
		require.NoError(t, err, "the target is back under the name the set records")
		assert.Equal(t, data, got)
	})
	t.Run("impossible", func(t *testing.T) {
		dir, _ := set(t, 5)
		require.NoError(t, os.Truncate(filepath.Join(dir, "movie.mkv"), 32<<10))
		res, err := repair(dir)
		require.NoError(t, err, "too few blocks is a verdict, not an error")
		assert.Equal(t, par2.RepairNotPossible, res.Status)
		assert.Positive(t, res.BlocksNeeded)
	})
}

// T1: par2's Process ignores its basepath; par2go sets it, so targets
// resolve against Dir, not the process's cwd.
func TestInProcessResolvesAgainstDirNotCwd(t *testing.T) {
	par2test.RequireLibrary(t)
	dir, data := set(t, 50)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "movie.mkv"), data[:len(data)/2], 0o644))
	t.Chdir(t.TempDir())
	res, err := repair(dir)
	require.NoError(t, err, "log: %s", res.Log)
	assert.Equal(t, par2.Repaired, res.Status)
	got, err := os.ReadFile(filepath.Join(dir, "movie.mkv"))
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestInProcessCancelReturnsPromptly(t *testing.T) {
	par2test.RequireLibrary(t)
	dir := t.TempDir()
	big := make([]byte, 64<<20)
	_, _ = rand.New(rand.NewSource(2)).Read(big)
	target := filepath.Join(dir, "movie.mkv")
	require.NoError(t, os.WriteFile(target, big, 0o644))
	par2test.CreateSet(t, dir, "movie.mkv.par2", []string{"movie.mkv"}, 64, 10, 0)
	// Damage it, so the run has a repair to do and cannot finish inside the
	// deadline on a fast disk by merely verifying.
	require.NoError(t, os.Truncate(target, 60<<20))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := par2child.InProcess{}.Run(ctx, par2child.Request{
		Op: par2child.OpRepair, Index: filepath.Join(dir, "movie.mkv.par2"), Dir: dir,
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), time.Second)
}

// The real child: this test binary, re-run as "par2-repair", loads the
// library in its own process and answers over the pipe.
func TestChildRunsTheLibrary(t *testing.T) {
	par2test.RequireLibrary(t)
	dir, _ := set(t, 20)
	child := par2child.Exec{Path: os.Args[0]}
	res, err := child.Run(context.Background(), par2child.Request{
		Op: par2child.OpRepair, Index: filepath.Join(dir, "movie.mkv.par2"), Dir: dir,
	})
	require.NoError(t, err)
	assert.Equal(t, par2.AllCorrect, res.Status)
	assert.Contains(t, res.Library, "libpar2shim", "the child names the library it loaded")
	require.NoError(t, child.Probe(context.Background()))
}
