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

package squasharr

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWorkerEngineIsFFmpegOrFFgo(t *testing.T) {
	for engine, ok := range map[string]bool{"": true, "ffmpeg": true, "ffgo": true, "gstreamer": false} {
		o := Options{WorkerEngine: engine}
		err := o.validateEngine()
		assert.Equal(t, ok, err == nil, "%q: %v", engine, err)
	}
}
