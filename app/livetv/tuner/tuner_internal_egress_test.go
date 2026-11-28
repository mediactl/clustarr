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

package tuner

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
)

// With egress tunnels the tuners are no more than the tunnels: argon-tv
// allows one stream per public IP (owner, 2026-11-27), and discover.json
// must not offer Plex a tuner no tunnel can carry.
func TestEgressTunnelsCapTheTuners(t *testing.T) {
	tunnels := func(n int) *EgressConfig {
		e := &EgressConfig{}
		for i := range n {
			e.Tunnels = append(e.Tunnels, EgressTunnel{Address: netip.AddrFrom4([4]byte{10, 100, 0, byte(21 + i)})})
		}
		return e
	}
	for name, tc := range map[string]struct {
		c    Config
		a    AccountState
		want int
	}{
		"no egress, the override":         {Config{Tuners: 3}, AccountState{}, 3},
		"no egress, the account":          {Config{}, AccountState{MaxConnections: 3}, 3},
		"two tunnels cap three tuners":    {Config{Tuners: 3, Egress: tunnels(2)}, AccountState{}, 2},
		"three tunnels, one tuner":        {Config{Tuners: 1, Egress: tunnels(3)}, AccountState{}, 1},
		"three tunnels, the account's 3":  {Config{Egress: tunnels(3)}, AccountState{MaxConnections: 3}, 3},
		"egress without tunnels, one":     {Config{Tuners: 3, Egress: tunnels(0)}, AccountState{}, 1},
		"no override, no account, egress": {Config{Egress: tunnels(3)}, AccountState{}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, effectiveTuners(tc.c, tc.a))
		})
	}
}
