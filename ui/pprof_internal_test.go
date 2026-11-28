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
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The ui serves net/http/pprof on its own address, and the heap profile
// answers there: what the 750 MB ui of 2026-11-28 could not be asked.
func TestThePprofAddressServesProfiles(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	servePprof(ctx, addr)
	require.Eventually(t, func() bool {
		resp, err := http.Get("http://" + addr + "/debug/pprof/heap?debug=1")
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 20*time.Millisecond)
}
