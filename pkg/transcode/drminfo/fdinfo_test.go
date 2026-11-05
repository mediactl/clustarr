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

package drminfo

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// arrowLake is talos-88's Arc 140T (i915, 2026-11-05): two video engines.
const arrowLake = `pos:	0
flags:	02100002
mnt_id:	30
ino:	1234
drm-driver:	i915
drm-client-id:	1587
drm-pdev:	0000:00:02.0
drm-engine-render:	0 ns
drm-engine-copy:	0 ns
drm-engine-video:	114960681264 ns
drm-engine-capacity-video:	2
drm-engine-video-enhance:	0 ns
drm-engine-compute:	0 ns
`

func TestParseReadsTheVideoEngineCount(t *testing.T) {
	assert.Equal(t, Info{Driver: "i915", VideoEngines: 2}, Parse(strings.NewReader(arrowLake)))
}

func TestAVideoEngineWithoutACapacityLineIsOne(t *testing.T) {
	one := "drm-driver:\ti915\ndrm-engine-render:\t0 ns\ndrm-engine-video:\t5 ns\n"
	assert.Equal(t, Info{Driver: "i915", VideoEngines: 1}, Parse(strings.NewReader(one)))
}

func TestNoDRMClientOrNoVideoEngineIsUnknown(t *testing.T) {
	assert.Equal(t, Info{}, Parse(strings.NewReader("pos:\t0\nflags:\t02\n")), "a plain file's fdinfo")
	assert.Equal(t, Info{Driver: "amdgpu"}, Parse(strings.NewReader("drm-driver:\tamdgpu\ndrm-engine-gfx:\t0 ns\n")), "no video engine named")
	assert.Equal(t, 0, VideoEngines(""), "no node")
	assert.Equal(t, 0, VideoEngines("/dev/null/missing"), "an unopenable node")
}
