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

package mediainfo_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMediainfoRunsNoProgram keeps pkg/mediainfo to the model and the mapping
// (spec §4.3 step 1.7). The manager links it for MediaInfo, Raw and
// ProbeVersion and never probes (R3), so no non-test file here may import
// os/exec or call go-ffprobe's Probe*. The ffprobe-backed probe is
// pkg/mediainfo/ffprobeexec.
func TestMediainfoRunsNoProgram(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	var checked int
	for _, p := range files {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		checked++
		ffprobeName := ""
		for _, imp := range f.Imports {
			ip, _ := strconv.Unquote(imp.Path.Value)
			switch ip {
			case "os/exec":
				t.Errorf("%s imports os/exec: the ffprobe exec lives in pkg/mediainfo/ffprobeexec", p)
			case "gopkg.in/vansante/go-ffprobe.v2":
				ffprobeName = "ffprobe"
				if imp.Name != nil {
					ffprobeName = imp.Name.Name
				}
			}
		}
		if ffprobeName == "" {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == ffprobeName && strings.HasPrefix(sel.Sel.Name, "Probe") {
				t.Errorf("%s calls %s.%s, which runs ffprobe: probe through pkg/mediainfo/ffprobeexec", p, x.Name, sel.Sel.Name)
			}
			return true
		})
	}
	require.Greater(t, checked, 5, "the walk found pkg/mediainfo's sources")
}
