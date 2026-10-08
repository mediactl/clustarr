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

package guards

// The repo-wide exec guard (spec 2026-10-06 §4.5.6, W5.14, W10.1): no
// production code runs an external program (ADR-0017), except the usenet
// engine's par2 repair child, which execs only this binary.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/test/installtest"
)

// execAllowed is the one production file that starts a process: the usenet
// engine's par2 repair child, whose only target is os.Executable() (spec
// §4.5.6; pkg/par2child's TestTheOnlyExecIsTheRepairChild holds that).
// Every other file under app/, pkg/, cmd/, internal/, ui/ and api/ runs no
// program (ADR-0017).
const execAllowed = "pkg/par2child/exec.go"

// execScanDirs are the production trees the exec guard reads; hack/ and
// test/ are out of scope (§4.5.6), and internal/ is in, since §4.5.6
// predates the tree the split created.
var execScanDirs = []string{"app", "pkg", "cmd", "ui", "api", "internal"}

// execGoFFprobe is go-ffprobe's import path: pkg/mediainfo keeps its types
// (the probe's model), never its three functions that run ffprobe.
const execGoFFprobe = "gopkg.in/vansante/go-ffprobe.v2"

// scanForExec reports every production file under root (non-test .go files
// in execScanDirs) that imports os/exec or calls syscall.Exec,
// syscall.ForkExec, syscall.StartProcess, os.StartProcess, x/sys/unix's
// Exec or ForkExec, or one of go-ffprobe's ProbeURL, ProbeReader and
// SetFFProbeBinPath, one finding per offending import and call; and how
// many files it read.
func scanForExec(root string) (found []string, checked int) {
	for _, dir := range execScanDirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) && p == filepath.Join(root, dir) {
					return filepath.SkipDir
				}
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if rel == execAllowed {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
			if err != nil {
				found = append(found, rel+": does not parse: "+err.Error())
				return nil
			}
			checked++
			found = append(found, execFindings(rel, f)...)
			return nil
		})
		if err != nil {
			found = append(found, dir+": "+err.Error())
		}
	}
	return found, checked
}

// execFindings is scanForExec's verdict on one parsed file.
func execFindings(rel string, f *ast.File) []string {
	var found []string
	aliases := map[string]string{} // local name -> import path
	for _, imp := range f.Imports {
		ip, _ := strconv.Unquote(imp.Path.Value)
		if ip == "os/exec" {
			found = append(found, rel+" imports os/exec: no production code runs an external program (ADR-0017)")
		}
		name := path.Base(ip)
		if ip == execGoFFprobe {
			name = "ffprobe"
		}
		if imp.Name != nil {
			name = imp.Name.Name
		}
		aliases[name] = ip
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch ip, fn := aliases[x.Name], sel.Sel.Name; {
		case ip == "syscall" && (fn == "Exec" || fn == "ForkExec" || fn == "StartProcess"),
			ip == "golang.org/x/sys/unix" && (fn == "Exec" || fn == "ForkExec"),
			ip == "os" && fn == "StartProcess",
			ip == execGoFFprobe && (fn == "ProbeURL" || fn == "ProbeReader" || fn == "SetFFProbeBinPath"):
			found = append(found, fmt.Sprintf("%s calls %s.%s: no production code starts a process (ADR-0017)", rel, x.Name, fn))
		}
		return true
	})
	return found
}

// No production code starts a process: no os/exec, no syscall or os
// process start, no go-ffprobe run (spec §4.5.6, R2, ADR-0017), except the
// repair child (execAllowed). It replaced the transcode worker's
// TestTheWorkerNeverExecsFFmpeg and pkg/mediainfo's half of it.
func TestNoProductionCodeStartsAProcess(t *testing.T) {
	found, checked := scanForExec(installtest.RepoRoot(t))
	for _, f := range found {
		t.Error(f)
	}
	require.Greater(t, checked, 500, "the walk found too few files; it is not looking where it thinks it is")
	_, err := os.Stat(filepath.Join(installtest.RepoRoot(t), execAllowed))
	require.NoError(t, err, "the one allowed exec moved: the allow-list names a file that is gone")
}

// execGuardWrite writes body at root/rel, for the guards' synthetic trees.
func execGuardWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
}

// The guard catches what it claims to, and its one exception is exactly
// the repair child (spec §4.5.6, W10.1): falsified on a synthetic tree, a
// sibling of the allowed file and every call shape included.
func TestTheExecGuardAllowsOnlyTheRepairChild(t *testing.T) {
	root := t.TempDir()
	execs := "package x\nimport \"os/exec\"\nfunc f() { _ = exec.Command(\"x\") }\n"
	execGuardWrite(t, root, "pkg/par2child/exec.go", execs)
	execGuardWrite(t, root, "pkg/par2child/other.go", execs)
	execGuardWrite(t, root, "pkg/download/usenet/repair.go", execs)
	execGuardWrite(t, root, "app/a/a.go", "package a\nimport \"syscall\"\nfunc f() { _ = syscall.Exec(\"\", nil, nil) }\n")
	execGuardWrite(t, root, "cmd/c/c.go", "package main\nimport \"os\"\nfunc main() { _, _ = os.StartProcess(\"\", nil, nil) }\n")
	execGuardWrite(t, root, "internal/i/i.go", "package i\nimport ffprobe \"gopkg.in/vansante/go-ffprobe.v2\"\nfunc f() { ffprobe.SetFFProbeBinPath(\"x\") }\n")
	execGuardWrite(t, root, "ui/u/u.go", "package u\nimport sc \"syscall\"\nfunc f() { _, _ = sc.ForkExec(\"\", nil, nil) }\n")
	execGuardWrite(t, root, "api/v/v.go", "package v\nimport probe \"gopkg.in/vansante/go-ffprobe.v2\"\nfunc f() { _, _ = probe.ProbeURL(nil, \"x\") }\n")
	execGuardWrite(t, root, "pkg/m/types.go", "package m\nimport ffprobe \"gopkg.in/vansante/go-ffprobe.v2\"\nvar _ *ffprobe.ProbeData\n") // types only
	execGuardWrite(t, root, "test/t/t.go", execs)                                                                                          // test/ is out of scope
	execGuardWrite(t, root, "hack/h/main.go", execs)                                                                                       // hack/ too
	execGuardWrite(t, root, "pkg/x/x_test.go", execs)
	found, checked := scanForExec(root)
	got := strings.Join(found, "\n")
	for _, want := range []string{
		"pkg/par2child/other.go imports os/exec", "pkg/download/usenet/repair.go imports os/exec",
		"app/a/a.go calls syscall.Exec", "cmd/c/c.go calls os.StartProcess",
		"internal/i/i.go calls ffprobe.SetFFProbeBinPath", "ui/u/u.go calls sc.ForkExec", "api/v/v.go calls probe.ProbeURL",
	} {
		assert.Contains(t, got, want)
	}
	for _, not := range []string{"pkg/par2child/exec.go", "test/t/t.go", "hack/h/main.go", "x_test.go", "pkg/m/types.go"} {
		assert.NotContains(t, got, not)
	}
	assert.Equal(t, 8, checked, "every production file but the allowed one is read")
}
