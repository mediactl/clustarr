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

package subtitleplan

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Moved from app/caption/worker/fetch with sidecarMode (F5.1): the loop
// plans the sidecar's mode and the task carries it.
func TestSidecarModeComesFromTheContainingRootFolder(t *testing.T) {
	root := func(path, mode string) catalogv1alpha1.RootFolder {
		var rf catalogv1alpha1.RootFolder
		rf.Spec.Path, rf.Spec.Permissions.FileMode = path, mode
		return rf
	}
	roots := []catalogv1alpha1.RootFolder{
		root("/data/media/movies", "0640"),
		root("/data/media/movies/kids", "0644"),
		root("/data/media/tv/", "0600"),
		root("/data/media/broken", "rw-r--r--"),
		root("/data/media/unset", ""),
	}
	for _, tc := range []struct {
		path     string
		fallback os.FileMode
		want     os.FileMode
	}{
		{"/data/media/movies/Film (2010)/Film (2010).mkv", 0, 0o640},
		{"/data/media/movies/kids/Cartoon (2001)/Cartoon (2001).mkv", 0, 0o644},
		{"/data/media/tv/Show/Season 01/Show - S01E01.mkv", 0, 0o600},
		{"/data/media/moviesextra/Film (2010).mkv", 0, DefaultSidecarMode},
		{"/data/media/broken/Film.mkv", 0, DefaultSidecarMode},
		{"/data/media/unset/Film.mkv", 0o660, 0o660},
		{"/data/elsewhere/Film.mkv", 0o600, 0o600},
	} {
		t.Run(tc.path, func(t *testing.T) {
			assert.Equal(t, tc.want, sidecarMode(roots, tc.path, tc.fallback))
		})
	}

	m, ok := parseFileMode("0775")
	require.True(t, ok)
	assert.Equal(t, os.FileMode(0o775), m)
	for _, bad := range []string{"", "775", "0778", "00775", "0x75"} {
		_, ok := parseFileMode(bad)
		assert.False(t, ok, bad)
	}
}
