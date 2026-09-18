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
