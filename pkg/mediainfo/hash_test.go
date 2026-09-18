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
	"time"

	"github.com/stretchr/testify/assert"
)

func TestProbeHash(t *testing.T) {
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	got := ProbeHash("/data/movies/Foo/Foo.mkv", 123456789, mtime)
	assert.Equal(t, "a9d82f8f4ea18a3f034b611e0122b9e23b8b5981", got)

	// Any of the three inputs changing must change the hash -- this is the
	// whole point of MediaFileStatus's "replan on probeHash change" contract.
	assert.NotEqual(t, got, ProbeHash("/data/movies/Foo/Foo.mkv", 123456790, mtime))
	assert.NotEqual(t, got, ProbeHash("/data/movies/Bar/Bar.mkv", 123456789, mtime))
	assert.NotEqual(t, got, ProbeHash("/data/movies/Foo/Foo.mkv", 123456789, mtime.Add(time.Second)))
}
