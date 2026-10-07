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

package subtitles_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestSubtitlesLinksNoSubtitleParser keeps go-astisub out of pkg/subtitles.
// The manager links pkg/subtitles for LangKey, Plan and the sidecar names
// (spec §4.1); parsing and rewriting subtitle files is the fetch worker's job,
// in pkg/subtitles/postprocess (§4.5.1).
func TestSubtitlesLinksNoSubtitleParser(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, "github.com/asticode") {
			t.Errorf("pkg/subtitles links %s; post-processing belongs in pkg/subtitles/postprocess", dep)
		}
	}
}
