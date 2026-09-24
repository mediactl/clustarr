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

package datapath

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocal(t *testing.T) {
	cases := []struct {
		name, dataDir, logical, want string
		wantErr                      bool
	}{
		{
			name: "mapped", dataDir: "/mnt/media", logical: "/data/movies/Film (2010)/Film.mkv",
			want: "/mnt/media/movies/Film (2010)/Film.mkv",
		},
		{name: "the root itself", dataDir: "/mnt/media", logical: "/data", want: "/mnt/media"},
		{name: "empty dataDir is the identity", logical: "/data/tv/x.mkv", want: "/data/tv/x.mkv"},
		{name: "escapes the volume", dataDir: "/mnt/media", logical: "/data/../etc/passwd", wantErr: true},
		{name: "a sibling of /data", dataDir: "/mnt/media", logical: "/database/x.mkv", wantErr: true},
		{name: "relative", dataDir: "/mnt/media", logical: "movies/x.mkv", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Local(tc.dataDir, tc.logical)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
