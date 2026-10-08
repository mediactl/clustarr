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

package ffruntime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Report names the files the dynamic loader actually mapped, so a host's
// system FFmpeg can never stand in for the pinned build (spec §7.4).
func TestMappedFindsTheLoadedLibraries(t *testing.T) {
	maps := []byte(strings.Join([]string{
		"7f00-7f01 r--p 00000000 08:01 1 /opt/native/lib/libavutil.so.60.3.100",
		"7f01-7f02 r-xp 00001000 08:01 2 /opt/native/lib/libavcodec.so.63.1.100",
		"7f02-7f03 r--p 00000000 08:01 3 /opt/native lib/libavformat.so.63.1.101",
		"7f03-7f04 r--p 00000000 00:00 0 ",
		"7f04-7f05 r--p 00000000 08:01 4 /usr/lib/libffshim.so",
	}, "\n"))
	assert.Equal(t, "/opt/native/lib/libavutil.so.60.3.100", mapped(maps, "libavutil.so"))
	assert.Equal(t, "/opt/native/lib/libavcodec.so.63.1.100", mapped(maps, "libavcodec.so"))
	assert.Equal(t, "/opt/native lib/libavformat.so.63.1.101", mapped(maps, "libavformat.so"), "a path with a space")
	assert.Equal(t, "/usr/lib/libffshim.so", mapped(maps, "libffshim.so"))
	assert.Empty(t, mapped(maps, "libswscale.so"))
}
