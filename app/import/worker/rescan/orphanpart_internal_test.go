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

package rescan

import (
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/app/squash/jobspec"
)

type partInfo struct {
	mode fs.FileMode
	mod  time.Time
}

func (i partInfo) Name() string       { return "" }
func (i partInfo) Size() int64        { return 1 }
func (i partInfo) Mode() fs.FileMode  { return i.mode }
func (i partInfo) ModTime() time.Time { return i.mod }
func (i partInfo) IsDir() bool        { return false }
func (i partInfo) Sys() any           { return nil }

// orphanPart decides one walked file: only a regular file of exactly the
// per-attempt transcode part form, unwritten for OrphanPartAge, whose job
// is not a live TranscodeJob.
func TestOrphanPartIsAnAbandonedTranscodeAttemptsOutputOnly(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	old := partInfo{mod: now.Add(-OrphanPartAge - time.Minute)}
	young := partInfo{mod: now.Add(-OrphanPartAge + time.Minute)}
	live := map[string]bool{"0123abcd": true}
	for name, c := range map[string]struct {
		path string
		info partInfo
		want bool
	}{
		"old, no live job":           {"/lib/M/Movie.part-deadbeef-1.mkv", old, true},
		"old, its job is live":       {"/lib/M/Movie.part-0123abcd-1.mkv", old, false},
		"young":                      {"/lib/M/Movie.part-deadbeef-1.mkv", young, false},
		"a torrent's .part":          {"/lib/M/Movie.mkv.part", old, false},
		"the generic transcode part": {"/lib/M/Movie.part.mkv", old, false},
		"a release named like one":   {"/lib/M/Movie.part-two.mkv", old, false},
		"media":                      {"/lib/M/Movie.mkv", old, false},
		"a symlink":                  {"/lib/M/Movie.part-deadbeef-1.mkv", partInfo{mode: fs.ModeSymlink, mod: old.mod}, false},
		"a directory":                {"/lib/M/Movie.part-deadbeef-1.mkv", partInfo{mode: fs.ModeDir, mod: old.mod}, false},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, orphanPart(c.path, c.info, now, live))
		})
	}
}

// The age outlives any attempt the default deadline lets run, with a day
// to spare: a part that old with no live job is no attempt's.
func TestOrphanPartAgeOutlivesTheDefaultDeadline(t *testing.T) {
	assert.GreaterOrEqual(t, OrphanPartAge, jobspec.DefaultActiveDeadline+24*time.Hour)
}
