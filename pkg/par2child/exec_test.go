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
	"context"
	"strings"
	"testing"
	"time"

	par2 "github.com/mediactl/par2go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/par2child"
)

func TestExecReturnsTheChildsResult(t *testing.T) {
	res, err := fake("echo").Run(context.Background(), par2child.Request{
		Op: par2child.OpRepair, Index: "/d/x.par2", Extras: []string{"/d/a", "/d/b"},
	})
	require.NoError(t, err)
	assert.Equal(t, par2.Repaired, res.Status)
	assert.Equal(t, 7, res.BlocksNeeded)
	assert.Equal(t, "/d/x.par2|/d/a|/d/b", res.Log)
}

// NZB names are arbitrary bytes; gob carries them, JSON would rewrite them.
func TestExecSendsNamesByteForByte(t *testing.T) {
	res, err := fake("echo").Run(context.Background(), par2child.Request{
		Op: par2child.OpRepair, Index: "/d/x.par2", Extras: []string{"/d/\xff.rar"},
	})
	require.NoError(t, err)
	assert.Equal(t, "/d/x.par2|/d/\xff.rar", res.Log)
}

// The parent's cancel closes the child's stdin; a child that answers then is
// heard, and its answer is kept beside the context's error.
func TestExecCancelsTheChildCooperatively(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	res, err := fake("cooperative").Run(ctx, par2child.Request{Op: par2child.OpRepair})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, par2child.ErrKindCancelled, res.ErrKind, "the child's own answer is kept")
}

// A child that ignores the closed stdin is killed once its grace has passed,
// so an uncancellable par2 phase cannot hold the job past its deadline.
func TestExecKillsAChildThatIgnoresCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := fake("stubborn").Run(ctx, par2child.Request{Op: par2child.OpRepair})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 3*time.Second, "SIGKILL at the deadline plus the 300 ms grace")
	assert.Zero(t, par2child.Unreaped(), "a killed child is reaped, not counted")
}

func TestExecReportsAChildThatDiedWithoutAResult(t *testing.T) {
	_, err := fake("die").Run(context.Background(), par2child.Request{Op: par2child.OpRepair})
	require.ErrorIs(t, err, par2child.ErrChildDied)
	assert.ErrorContains(t, err, "boom: the child crashed", "the stderr tail")
}

func TestExecReportsAMissingBinary(t *testing.T) {
	missing := par2child.Exec{Path: "/nonexistent/agent"}
	_, err := missing.Run(context.Background(), par2child.Request{Op: par2child.OpProbe})
	require.ErrorIs(t, err, par2.ErrUnavailable)
	assert.ErrorIs(t, missing.Probe(context.Background()), par2.ErrUnavailable)
}

// The child gets PAR2GO_LIB, LD_LIBRARY_PATH, TMPDIR, GODEBUG and
// GOMAXPROCS, and nothing else (§8.4.3): no credentials, no NATS or
// Kubernetes address.
func TestExecPassesTheChildOnlyItsEnvironment(t *testing.T) {
	t.Setenv("CLUSTARR_SECRET", "s3cret")
	t.Setenv("NATS_URL", "nats://nats:4222")
	t.Setenv("PAR2GO_LIB", "/opt/libpar2shim.so")
	t.Setenv("TMPDIR", t.TempDir())
	e := fake("env")
	e.Env = nil // childEnv()
	res, err := e.Run(context.Background(), par2child.Request{Op: par2child.OpRepair})
	require.NoError(t, err)
	assert.Contains(t, res.Log, "PAR2GO_LIB=/opt/libpar2shim.so")
	assert.Contains(t, res.Log, "TMPDIR=")
	assert.NotContains(t, res.Log, "CLUSTARR_SECRET")
	assert.NotContains(t, res.Log, "NATS_URL")
	allowed := map[string]bool{"PAR2GO_LIB": true, "LD_LIBRARY_PATH": true, "TMPDIR": true, "GODEBUG": true, "GOMAXPROCS": true}
	for line := range strings.SplitSeq(res.Log, "\n") {
		key, _, _ := strings.Cut(line, "=")
		assert.True(t, allowed[key], "the child got %q", line)
	}
}
