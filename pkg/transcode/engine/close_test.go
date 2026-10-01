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

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A write error the muxer reports only when it closes its output (a full
// disk or a lost NFS server, surfacing at the final flush) fails the run
// and removes the output, like any other failure: the worker must never
// verify and swap in a file whose last bytes may not have been written.
func TestRunFailsWhenClosingTheMuxerFails(t *testing.T) {
	src := everythingClip(t, 1)
	out := filepath.Join(t.TempDir(), "out.part.mkv")

	eio := errors.New("input/output error")
	old := closeMuxer
	t.Cleanup(func() { closeMuxer = old })
	closeMuxer = func(m *ffgo.Muxer) error {
		require.NoError(t, old(m))
		return eio
	}

	_, err := Run(context.Background(), remuxPlan(), src, out, Options{})
	require.ErrorIs(t, err, eio)
	var ee *Error
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, "mux", ee.Stage)
	_, statErr := os.Stat(out)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "a failed run leaves no output")
}

// A run that fails for another reason keeps that reason: closing the
// muxer afterwards never replaces it.
func TestAMuxerCloseErrorNeverReplacesTheRunsOwnFailure(t *testing.T) {
	src := everythingClip(t, 1)
	out := filepath.Join(t.TempDir(), "out.part.mkv")

	old := closeMuxer
	t.Cleanup(func() { closeMuxer = old })
	closeMuxer = func(m *ffgo.Muxer) error {
		_ = old(m)
		return errors.New("close failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, remuxPlan(), src, out, Options{})
	require.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, err.Error(), "close failed")
}
