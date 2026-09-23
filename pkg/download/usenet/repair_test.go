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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// par2Binary is where par2cmdline-turbo lives in images/Dockerfile.media. A
// developer box usually has neither, so every test below skips rather than
// fails -- the same contract pkg/transcode's tests have with ffmpeg.
func par2Binary(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("par2")
	if err != nil {
		t.Skip("par2 not present on this box")
	}
	return p
}

func TestPar2RunnerReportsAMissingBinary(t *testing.T) {
	r := Par2Runner{Path: "definitely-not-a-real-par2-binary"}
	require.False(t, r.Available())
	_, err := r.Repair(context.Background(), t.TempDir(), "x.par2")
	require.ErrorIs(t, err, ErrPar2Unavailable,
		"a missing binary is a deployment problem, not an unrepairable download")
}

func TestPar2RunnerVerifiesAnIntactSet(t *testing.T) {
	bin := par2Binary(t)
	dir := t.TempDir()

	data := make([]byte, 128<<10)
	rnd := rand.New(rand.NewSource(1))
	_, _ = rnd.Read(data)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "movie.mkv"), data, 0o644))

	// -b caps the block count: par2cmdline's default of 2000 blocks over a
	// small file makes the Reed-Solomon matrix enormous and the test minutes
	// long, which is a property of the tool and not of anything under test.
	create := exec.Command(bin, "c", "-q", "-b16", "-r20", "--", "movie.mkv.par2", "movie.mkv")
	create.Dir = dir
	out, err := create.CombinedOutput()
	require.NoErrorf(t, err, "par2 create failed: %s", out)

	res, err := Par2Runner{Path: bin}.Repair(context.Background(), dir, "movie.mkv.par2")
	require.NoError(t, err)
	require.True(t, res.AllCorrect, "an intact set must verify without repair: %s", res.Output)
	require.False(t, res.Repaired)
}

func TestPar2RunnerRepairsADamagedFile(t *testing.T) {
	bin := par2Binary(t)
	dir := t.TempDir()

	data := make([]byte, 128<<10)
	rnd := rand.New(rand.NewSource(2))
	_, _ = rnd.Read(data)
	target := filepath.Join(dir, "movie.mkv")
	require.NoError(t, os.WriteFile(target, data, 0o644))

	create := exec.Command(bin, "c", "-q", "-b16", "-r50", "--", "movie.mkv.par2", "movie.mkv")
	create.Dir = dir
	out, err := create.CombinedOutput()
	require.NoErrorf(t, err, "par2 create failed: %s", out)

	// Corrupt a slice in the middle.
	damaged := append([]byte(nil), data...)
	for i := 40 << 10; i < 44<<10; i++ {
		damaged[i] ^= 0xFF
	}
	require.NoError(t, os.WriteFile(target, damaged, 0o644))

	res, err := Par2Runner{Path: bin}.Repair(context.Background(), dir, "movie.mkv.par2")
	require.NoError(t, err, "output: %s", res.Output)
	require.True(t, res.Repaired, "output: %s", res.Output)

	fixed, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, data, fixed, "the repaired file must match the original byte for byte")
}

func TestTailWriterBoundsASubprocessThatTalksTooMuch(t *testing.T) {
	// The bound must not fail the write: an io.Writer that refuses output
	// makes exec.Cmd.Run return THAT error instead of par2's verdict.
	w := &tailWriter{max: 16}
	n, err := w.Write([]byte(strings.Repeat("a", 1000)))
	require.NoError(t, err)
	require.Equal(t, 1000, n, "the writer must claim every byte it was given")
	require.Len(t, w.String(), 16)

	n, err = w.Write([]byte("bbbb"))
	require.NoError(t, err)
	require.Equal(t, 4, n)
	require.Equal(t, 16, len(w.String()))
	require.True(t, strings.HasSuffix(w.String(), "bbbb"), "the tail is what matters, got %q", w.String())
}

func TestTailTruncatesFromTheFront(t *testing.T) {
	require.Equal(t, "short", tail("  short  ", 100))
	require.Equal(t, "..."+strings.Repeat("z", 5), tail(strings.Repeat("y", 3)+strings.Repeat("z", 5), 5))
}
