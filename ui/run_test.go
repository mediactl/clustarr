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

package ui_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/pipeline"
	"github.com/mediactl/clustarr/ui"
)

// freeAddr reserves an ephemeral TCP port and immediately releases it, so
// Run can be given a real address the test can dial. There is a small window
// between the Close below and Run's own Listen where another process could
// take the port; that is the standard, widely used way to hand a test server
// a free port without changing Run's signature to expose its listener.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// TestRunEndsAnOpenSSEStreamOnContextCancel is the regression test for the
// bug the review found: without http.Server.BaseContext tied to Run's own
// ctx, a request's context is only ever cancelled by the client disconnecting,
// so Shutdown (which waits for active connections to finish rather than
// interrupting them) would burn the entire shutdownGrace on any client that
// still has /events/pipeline open, and the handler goroutine would outlive
// Run's return. With BaseContext wired to ctx, cancelling ctx cancels every
// in-flight request's context immediately (context.WithCancel propagates a
// parent's cancellation to every context derived from it), so the SSE
// handler's `case <-ctx.Done(): return` fires right away, the connection goes
// idle, and Shutdown returns almost immediately.
func TestRunEndsAnOpenSSEStreamOnContextCancel(t *testing.T) {
	addr := freeAddr(t)
	entries := func(context.Context) []pipeline.Entry {
		return []pipeline.Entry{{Title: "The Shawshank Redemption", Stage: pipeline.StageDownloading}}
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	runErr := make(chan error, 1)
	go func() {
		runErr <- ui.Run(runCtx, ui.Options{BindAddress: addr, AuthMode: ui.AuthModeAnonymous, Entries: entries})
	}()

	// Wait for the listener to come up.
	require.Eventually(t, func() bool {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 2*time.Second, 10*time.Millisecond, "server never came up on %s", addr)

	// Open the SSE stream and keep draining it in the background so we can
	// tell when the server closes it from its side.
	streamReq, err := http.NewRequest(http.MethodGet, "http://"+addr+"/events/pipeline", nil)
	require.NoError(t, err)
	streamResp, err := http.DefaultClient.Do(streamReq)
	require.NoError(t, err)
	defer func() { _ = streamResp.Body.Close() }()
	require.Equal(t, http.StatusOK, streamResp.StatusCode)

	streamEnded := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, streamResp.Body)
		close(streamEnded)
	}()

	// Give the client a moment to actually be attached to the stream before
	// cancelling, so the test exercises "shutdown with a client connected"
	// rather than racing the connection's own setup.
	time.Sleep(50 * time.Millisecond)

	const wellUnderShutdownGrace = 2 * time.Second // shutdownGrace is 5s
	start := time.Now()
	cancelRun()

	select {
	case err := <-runErr:
		require.NoError(t, err)
	case <-time.After(wellUnderShutdownGrace):
		t.Fatal("ui.Run did not return within " + wellUnderShutdownGrace.String() + " of context cancellation")
	}
	require.Less(t, time.Since(start), wellUnderShutdownGrace,
		"ui.Run took too long to shut down; BaseContext may not be wired to ctx")

	select {
	case <-streamEnded:
	case <-time.After(wellUnderShutdownGrace):
		t.Fatal("SSE stream was not closed by the server after shutdown")
	}
}
