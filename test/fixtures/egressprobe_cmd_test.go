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

package main

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The probe passes only when every --reach address connects and no
// --refuse address does, so neither a pod that reaches the Internet nor
// one that can reach nothing at all reads as an enforced policy.
func TestTheEgressProbePassesOnlyOnReachAndRefuse(t *testing.T) {
	open, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = open.Close() })
	go func() {
		for {
			c, err := open.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	shut := closed.Addr().String()
	require.NoError(t, closed.Close())

	up := open.Addr().String()
	probe := func(reach, refuse []string) error {
		return egressProbe(io.Discard, reach, refuse, 2*time.Second)
	}
	require.NoError(t, probe([]string{up}, []string{shut}))
	require.Error(t, probe([]string{up}, []string{up}), "a refuse address that connects must fail the probe")
	require.Error(t, probe([]string{shut}, []string{shut}), "a probe that reaches nothing must fail")
}
