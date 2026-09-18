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

package mediainfo

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMasteringDisplayX265RendersTheVerifiedNoteExample(t *testing.T) {
	m := MasteringDisplay{
		GreenX: 13250, GreenY: 34500, BlueX: 7500, BlueY: 3000,
		RedX: 34000, RedY: 16000, WhiteX: 15635, WhiteY: 16450,
		MaxLuminance: 10000000, MinLuminance: 1,
	}
	require.Equal(t, "G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1)", m.X265())
}

func TestContentLightX265RendersMaxCLLThenMaxFALL(t *testing.T) {
	require.Equal(t, "1000,400", ContentLight{MaxCLL: 1000, MaxFALL: 400}.X265())
}
