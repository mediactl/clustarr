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

package ui

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestASilentConnectionAcceptedAfterTheSweepIsClosedAtOnce: Run's
// silent.closeAll runs before httpSrv.Shutdown closes the listeners, so a
// connection the accept loop hands the ConnState hook in that window would
// join the set after the sweep and hold Shutdown for net/http's 5 s
// StateNew rule -- the race closeAll removes, open for the width of that
// window. closeAll marks the set closing, and a StateNew that arrives
// afterwards is closed on the spot and never kept.
func TestASilentConnectionAcceptedAfterTheSweepIsClosedAtOnce(t *testing.T) {
	var s silentConns

	before, beforePeer := net.Pipe()
	defer func() { _ = beforePeer.Close() }()
	s.track(before, http.StateNew)
	s.closeAll()
	_, err := before.Read(make([]byte, 1))
	require.ErrorIs(t, err, io.ErrClosedPipe, "the sweep closes a connection it held")

	late, latePeer := net.Pipe()
	defer func() { _ = latePeer.Close() }()
	// A deadline so a connection the hook kept open fails here by name
	// rather than blocking the read for good.
	require.NoError(t, late.SetReadDeadline(time.Now().Add(time.Second)))
	s.track(late, http.StateNew)
	_, err = late.Read(make([]byte, 1))
	require.ErrorIs(t, err, io.ErrClosedPipe, "a connection accepted after the sweep is closed as it arrives")

	s.mu.Lock()
	kept := len(s.conns)
	s.mu.Unlock()
	require.Zero(t, kept, "nothing joins the set once it is closing")
}
