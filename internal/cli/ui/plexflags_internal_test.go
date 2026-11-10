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

package uicli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPlexMusicProviderFlagParsesAndDefaultsOff: the chart renders
// --plex-music-provider=true under ui.plex.musicProvider and the command
// must know the flag (cmd/ui's manifest test executes a rendered argv
// through it); its default is false, since no PMS build accepts a provider
// declaring types 8, 9 and 10 (2026-11-10 design §7.6).
func TestPlexMusicProviderFlagParsesAndDefaultsOff(t *testing.T) {
	cmd := NewCommandWith(nil)
	f := cmd.Flags().Lookup("plex-music-provider")
	require.NotNil(t, f, "the flag exists")
	require.Equal(t, "false", f.DefValue)
	require.NoError(t, cmd.Flags().Parse([]string{"--plex-music-provider=true"}))
	require.Equal(t, "true", f.Value.String())
}
