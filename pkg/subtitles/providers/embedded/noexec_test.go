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

package embedded_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEmbeddedRunsNoProgram: the manager links this package for Search
// (subtitlerequest's extractableStreams, providerset's prototype), so it
// must not exec. The ffmpeg extractor is embedded/execextract (spec §7.3.1).
func TestEmbeddedRunsNoProgram(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	var checked int
	for _, p := range files {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		require.NoError(t, err)
		checked++
		for _, imp := range f.Imports {
			if imp.Path.Value == `"os/exec"` {
				t.Errorf("%s imports os/exec: the extractor that runs ffmpeg is embedded/execextract", p)
			}
		}
	}
	require.Positive(t, checked)
}
