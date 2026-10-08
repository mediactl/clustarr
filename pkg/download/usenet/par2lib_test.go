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
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	par2 "github.com/mediactl/par2go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/par2child"
	"github.com/mediactl/clustarr/test/par2test"
)

// Every way a library run can end maps onto the verdict the CLI's exit codes
// and output gave (spec 2026-10-06 §8.5.3), with OD22: out of memory and a
// child that died without a result say nothing about the release, so they
// are local faults that never blocklist.
func TestPar2VerdictMapsEveryOutcome(t *testing.T) {
	log := strings.Repeat("x", 3000) + "You need 6 more recovery blocks to be able to repair."
	for _, tt := range []struct {
		name    string
		res     par2child.Result
		err     error
		want    Par2Result
		wantErr []error
		notErr  []error
		reason  commonv1alpha1.DownloadFailureReason
	}{
		{name: "all correct", res: par2child.Result{Status: par2.AllCorrect}, want: Par2Result{AllCorrect: true}},
		{name: "repaired", res: par2child.Result{Status: par2.Repaired}, want: Par2Result{Repaired: true}},
		{
			name: "not possible", res: par2child.Result{Status: par2.RepairNotPossible, BlocksNeeded: 6, Log: log},
			want:    Par2Result{BlocksNeeded: 6, Output: tail(log, 2048)},
			wantErr: []error{ErrRepairFailed}, reason: commonv1alpha1.DownloadFailureMissingArticles,
		},
		{
			name: "repair possible but not done", res: par2child.Result{Status: par2.RepairPossible},
			wantErr: []error{ErrRepairFailed}, reason: commonv1alpha1.DownloadFailureMissingArticles,
		},
		{
			name: "insufficient critical data", err: fmt.Errorf("%w: x", par2.ErrInsufficientCriticalData),
			wantErr: []error{ErrRepairFailed, par2.ErrInsufficientCriticalData}, reason: commonv1alpha1.DownloadFailureMissingArticles,
		},
		{name: "io", err: par2.ErrIO, wantErr: []error{ErrRepairFailed}, reason: commonv1alpha1.DownloadFailureMissingArticles},
		{name: "logic", err: par2.ErrLogic, wantErr: []error{ErrRepairFailed}, reason: commonv1alpha1.DownloadFailureMissingArticles},
		{name: "repair failed", err: par2.ErrRepairFailed, wantErr: []error{ErrRepairFailed}, reason: commonv1alpha1.DownloadFailureMissingArticles},
		// OD22: no evidence against the release.
		{
			name: "out of memory", err: par2.ErrMemory,
			wantErr: []error{ErrPar2Crashed}, notErr: []error{ErrRepairFailed}, reason: commonv1alpha1.DownloadFailureWriteError,
		},
		{
			name: "child died", err: fmt.Errorf("%w: signal: killed", par2child.ErrChildDied),
			wantErr: []error{ErrPar2Crashed}, notErr: []error{ErrRepairFailed}, reason: commonv1alpha1.DownloadFailureWriteError,
		},
		{name: "invalid options", err: par2.ErrInvalidOptions, notErr: []error{ErrRepairFailed}, reason: commonv1alpha1.DownloadFailureWriteError},
		{
			name: "unavailable", err: par2.ErrUnavailable,
			wantErr: []error{ErrPar2Unavailable}, notErr: []error{ErrRepairFailed}, reason: commonv1alpha1.DownloadFailureWriteError,
		},
		{
			name: "cancelled", err: fmt.Errorf("par2child: %w", context.Canceled),
			wantErr: []error{context.Canceled}, notErr: []error{ErrRepairFailed},
		},
		{
			name: "deadline", err: fmt.Errorf("par2child: %w", context.DeadlineExceeded),
			wantErr: []error{context.DeadlineExceeded}, notErr: []error{ErrRepairFailed}, reason: commonv1alpha1.DownloadFailureTimeout,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := par2Verdict(tt.res, tt.err)
			if tt.wantErr == nil && tt.notErr == nil {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
				return
			}
			require.Error(t, err)
			for _, w := range tt.wantErr {
				assert.ErrorIs(t, err, w)
			}
			for _, n := range tt.notErr {
				assert.NotErrorIs(t, err, n)
			}
			if tt.want.BlocksNeeded > 0 {
				assert.Equal(t, tt.want, got)
				assert.ErrorContains(t, err, "6 more recovery blocks needed")
				assert.LessOrEqual(t, len(got.Output), 2048+len("..."), "the log is bounded")
			}
			if tt.reason != "" {
				assert.Equal(t, tt.reason, failureReason(context.Background(), err))
			}
		})
	}
}

// par2Extras is the CLI's extra-file filter (exists, inside the base path,
// non-empty) as absolute paths: par2go wants absolute paths under Dir.
func TestPar2ExtrasAreAbsoluteAndSkipEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"a.rar": "x", "empty.rar": "", "x.par2": "p", "b.mkv": "y"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	assert.Equal(t, []string{filepath.Join(dir, "a.rar"), filepath.Join(dir, "b.mkv")}, par2Extras(dir, "x.par2"))
	assert.Nil(t, par2Extras(filepath.Join(dir, "missing"), "x.par2"))
}

// A repairer whose child cannot start reports the library unavailable: a
// local fault (writeError), never a verdict on the release.
func TestLibraryRepairerReportsAMissingLibrary(t *testing.T) {
	r := &LibraryRepairer{Runner: par2child.Exec{Path: "/nonexistent/agent"}}
	assert.False(t, r.Available())
	_, err := r.Repair(context.Background(), t.TempDir(), "x.par2")
	require.ErrorIs(t, err, ErrPar2Unavailable)
	assert.NotErrorIs(t, err, ErrRepairFailed)
	assert.Equal(t, commonv1alpha1.DownloadFailureWriteError, failureReason(context.Background(), err))
}

// T1: par2's Process ignores basepath; the job's targets must resolve
// against the job directory whatever the engine's cwd. The directory is
// passed relative, as the client could, so Repair must make it absolute.
func TestLibraryRepairerResolvesTargetsAgainstTheJobDir(t *testing.T) {
	r := libraryRepairer(t)
	parent := t.TempDir()
	dir := filepath.Join(parent, "job")
	require.NoError(t, os.Mkdir(dir, 0o755))
	data := bytes.Repeat([]byte("clustarr"), 16<<10)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "movie.mkv"), data, 0o644))
	par2test.CreateSet(t, dir, "movie.mkv.par2", []string{"movie.mkv"}, 16, 50, 0)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "movie.mkv"), data[:len(data)/2], 0o644))

	t.Chdir(parent)
	res, err := r.Repair(context.Background(), "job", "movie.mkv.par2")
	require.NoError(t, err, "output: %s", res.Output)
	assert.True(t, res.Repaired)
	got, err := os.ReadFile(filepath.Join(dir, "movie.mkv"))
	require.NoError(t, err)
	assert.Equal(t, data, got)
	assert.NoFileExists(t, filepath.Join(parent, "movie.mkv"), "nothing is written into the cwd")
}

// A set short of recovery blocks is a release fault, and the verdict says
// how many more blocks it needed.
func TestLibraryRepairerCountsTheBlocksItNeeds(t *testing.T) {
	r := libraryRepairer(t)
	dir := t.TempDir()
	data := make([]byte, 128<<10)
	_, _ = rand.New(rand.NewSource(3)).Read(data)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "movie.mkv"), data, 0o644))
	par2test.CreateSet(t, dir, "movie.mkv.par2", []string{"movie.mkv"}, 16, 12, 0)
	require.NoError(t, os.Truncate(filepath.Join(dir, "movie.mkv"), 32<<10))

	res, err := r.Repair(context.Background(), dir, "movie.mkv.par2")
	require.ErrorIs(t, err, ErrRepairFailed)
	assert.Positive(t, res.BlocksNeeded)
	assert.ErrorContains(t, err, fmt.Sprintf("%d more recovery blocks needed", res.BlocksNeeded))
	assert.Equal(t, commonv1alpha1.DownloadFailureMissingArticles, failureReason(context.Background(), err))
}
