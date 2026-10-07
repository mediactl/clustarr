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

package providerset_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManagerSideCaptionLinksNoSubtitleClient holds spec §4.3 P1: the
// SubtitleProvider, SubtitleProfile and SubtitleRequest reconcilers, and the
// providerset they read, link no remote subtitle client, archive reader or
// subtitle post-processing (§13 OD9). `test/guards` (W5.14) overlaps it.
func TestManagerSideCaptionLinksNoSubtitleClient(t *testing.T) {
	denied := []string{
		"github.com/mediactl/clustarr/pkg/subtitles/providers/opensubtitlescom",
		"github.com/mediactl/clustarr/pkg/subtitles/providers/gestdown",
		"github.com/mediactl/clustarr/pkg/subtitles/providers/subdl",
		"github.com/mediactl/clustarr/pkg/subtitles/providers/subsource",
		"github.com/mediactl/clustarr/pkg/subtitles/providers/internal",
		"github.com/mediactl/clustarr/pkg/subtitles/providers/embedded/execextract",
		"github.com/mediactl/clustarr/app/caption/providerset/build",
		"github.com/mediactl/clustarr/app/caption/worker",
		"github.com/nwaples/rardecode",
		"github.com/asticode",
	}
	for _, pkg := range []string{
		"github.com/mediactl/clustarr/app/caption/providerset",
		"github.com/mediactl/clustarr/app/caption/controller/...",
	} {
		out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
		require.NoError(t, err, "go list -deps %s:\n%s", pkg, out)
		for _, dep := range strings.Fields(string(out)) {
			for _, bad := range denied {
				if dep == bad || strings.HasPrefix(dep, bad+"/") {
					assert.Failf(t, "links agent code", "%s links %s", pkg, dep)
				}
			}
		}
	}
}
