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
	"testing"

	"github.com/stretchr/testify/assert"
)

// With a pool of User-Agents the tuners are no more than the pool:
// argon-tv allows one stream per public IP and User-Agent (owner,
// 2026-11-28), and discover.json must not offer Plex a tuner whose stream
// would knock another off.
func TestAPoolOfUserAgentsCapsTheTuners(t *testing.T) {
	pool := func(n int) []string { return []string{"a", "b", "c", "d"}[:n] }
	for name, tc := range map[string]struct {
		c    Config
		a    AccountState
		want int
	}{
		"no pool, the override":          {Config{Tuners: 3}, AccountState{}, 3},
		"no pool, the account":           {Config{}, AccountState{MaxConnections: 3}, 3},
		"two agents cap three tuners":    {Config{Tuners: 3, UserAgents: pool(2)}, AccountState{}, 2},
		"three agents, one tuner":        {Config{Tuners: 1, UserAgents: pool(3)}, AccountState{}, 1},
		"three agents, the account's 3":  {Config{UserAgents: pool(3)}, AccountState{MaxConnections: 3}, 3},
		"four agents, the account's 3":   {Config{UserAgents: pool(4)}, AccountState{MaxConnections: 3}, 3},
		"no override, no account, three": {Config{UserAgents: pool(3)}, AccountState{}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, effectiveTuners(tc.c, tc.a))
		})
	}
}
