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

package squasharr

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTheWorkerNeverExecsFFmpeg holds squasharr to the in-process engine
// (ffgo Phase 5): no non-test file under app/squash, pkg/transcode or
// cmd/squasharr-worker imports os/exec, or calls the probes that shell out
// to ffprobe (pkg/mediainfo's Probe and ProbeAudio, go-ffprobe's Probe*).
// The transcoder image carries no ffmpeg or ffprobe executable, so such a
// call would fail only in a pool pod, at a job; this fails it here.
func TestTheWorkerNeverExecsFFmpeg(t *testing.T) {
	banned := map[string]func(name string) bool{
		"github.com/mediactl/clustarr/pkg/mediainfo": func(n string) bool { return n == "Probe" || n == "ProbeAudio" },
		"gopkg.in/vansante/go-ffprobe.v2":            func(n string) bool { return strings.HasPrefix(n, "Probe") },
	}
	var checked int
	for _, root := range []string{".", "../../pkg/transcode", "../../cmd/squasharr-worker"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			checked++
			local := map[string]func(string) bool{}
			for _, imp := range f.Imports {
				ip, _ := strconv.Unquote(imp.Path.Value)
				if ip == "os/exec" {
					t.Errorf("%s imports os/exec: the worker runs FFmpeg in-process", p)
				}
				if is, ok := banned[ip]; ok {
					name := strings.TrimSuffix(path.Base(ip), ".v2")
					name = strings.TrimPrefix(name, "go-")
					if imp.Name != nil {
						name = imp.Name.Name
					}
					local[name] = is
				}
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
				if x, ok := sel.X.(*ast.Ident); ok && local[x.Name] != nil && local[x.Name](sel.Sel.Name) {
					t.Errorf("%s calls %s.%s, which runs ffprobe: probe through the worker's Engine", p, x.Name, sel.Sel.Name)
				}
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}
	require.Greater(t, checked, 50, "the walk found the worker's sources")
}
