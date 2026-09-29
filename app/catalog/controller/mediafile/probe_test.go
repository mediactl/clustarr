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

package mediafile

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/mediainfo"
)

func TestEvaluateProbe(t *testing.T) {
	path := "/data/media/movies/Inception (2010)/Inception (2010).mkv"
	mt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	fresh := evaluateProbe(path, 1024, mt, "")
	assert.True(t, fresh.Stale, "empty status hash is always stale")
	assert.NotEmpty(t, fresh.Hash)

	same := evaluateProbe(path, 1024, mt, fresh.Hash)
	assert.False(t, same.Stale, "matching hash is not stale")

	changed := evaluateProbe(path, 2048, mt, fresh.Hash)
	assert.True(t, changed.Stale, "size change makes the hash stale")

	assert.True(t, fresh.ModTime.Time.Equal(mt))
	assert.Equal(t, int64(1024), fresh.SizeBytes)
}

// TestProbeDueForAnOlderProbeVersion: a file probed before the probe
// learned a field (videoEncoder, 2026-09-29) is probed again even though
// its bytes -- and so its probeHash -- are unchanged. The hash stays put:
// captionarr and squasharr read a new hash as a different file.
func TestProbeDueForAnOlderProbeVersion(t *testing.T) {
	path := "/data/media/movies/Inception (2010)/Inception (2010).mkv"
	mt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	ps := evaluateProbe(path, 1024, mt, "")
	current := ps.Hash
	ps = evaluateProbe(path, 1024, mt, current)
	assert.False(t, ps.Stale)
	assert.True(t, probeDue(current, 0, ps), "probed by a version that did not record everything")
	assert.False(t, probeDue(current, mediainfo.ProbeVersion, ps), "probed by this version, unchanged")
	assert.True(t, probeDue("", mediainfo.ProbeVersion, ps), "never probed")
	assert.Positive(t, mediainfo.ProbeVersion)
}
