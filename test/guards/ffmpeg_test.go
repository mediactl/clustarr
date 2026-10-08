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

// The ffmpeg guards (spec 2026-10-06 W10.9-W10.11; upgrade guide U2): no
// Go file, test or tool included, runs or locates the ffmpeg or ffprobe
// CLI; no script runs it; the test environment and CI install neither; and
// the e2e fixtures image bakes its clips on ffgo (owner decision
// 2026-10-06, ADR-0017). The literals naming the two programs are split
// throughout this file ("ff"+"mpeg") so it does not trip its own guard.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/test/installtest"
)

// ffmpegNames are the CLIs no Go file may run (owner, 2026-10-06; U2).
var ffmpegNames = []string{"ff" + "mpeg", "ff" + "probe"}

// ffmpegLocators are the calls that run or locate a program by name.
var ffmpegLocators = map[string]bool{"Command": true, "CommandContext": true, "LookPath": true, "Tool": true, "FindTool": true}

// ffmpegSkipDir reports whether a walk over the repository skips dir: VCS
// and tool caches, hidden directories (worktrees under .claude among them)
// and any nested module, which is another tree's code.
func ffmpegSkipDir(root, dir string, d fs.DirEntry) bool {
	if dir == root {
		return false
	}
	switch name := d.Name(); {
	case strings.HasPrefix(name, "."), name == "vendor", name == "node_modules":
		return true
	}
	_, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil
}

// ffmpegNamedIn reports whether e names ffmpeg or ffprobe: a string literal
// that is one (bare or as a path's last element) or whose first word is
// one ("ff…mpeg -i x" handed to a shell), or a call any of whose arguments
// does (exec.Command(Tool(t, …)), filepath.Join(dir, …)).
func ffmpegNamedIn(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return false
		}
		v, err := strconv.Unquote(x.Value)
		if err != nil {
			return false
		}
		if slices.Contains(ffmpegNames, filepath.Base(v)) {
			return true
		}
		fields := strings.Fields(v)
		return len(fields) > 1 && slices.Contains(ffmpegNames, filepath.Base(fields[0]))
	case *ast.CallExpr:
		return slices.ContainsFunc(x.Args, ffmpegNamedIn)
	}
	return false
}

// ffmpegCallee is the name a call calls: f for f(…) and x.f(…), through a
// generic instantiation.
func ffmpegCallee(call *ast.CallExpr) string {
	switch fn := unindex(call.Fun).(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name
	case *ast.Ident:
		return fn.Name
	}
	return ""
}

// ffmpegForwarders is every function in file that passes one of its own
// parameters as the program of exec.Command or exec.CommandContext: a
// helper such as run(t, name, args...) runs whatever its callers name.
func ffmpegForwarders(file *ast.File) []string {
	var out []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Type.Params == nil {
			continue
		}
		params := map[string]bool{}
		for _, f := range fn.Type.Params.List {
			for _, n := range f.Names {
				params[n.Name] = true
			}
		}
		forwards := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || forwards {
				return !forwards
			}
			program := -1
			switch ffmpegCallee(call) {
			case "Command":
				program = 0
			case "CommandContext":
				program = 1
			}
			if program >= 0 && program < len(call.Args) {
				if id, ok := call.Args[program].(*ast.Ident); ok && params[id.Name] {
					forwards = true
				}
			}
			return true
		})
		if forwards {
			out = append(out, fn.Name.Name)
		}
	}
	return out
}

// scanForFFmpegCLI reports every place under root, in every .go file
// (tests, test/ and hack/ included), that runs or locates the ffmpeg or
// ffprobe CLI: a Command/CommandContext/LookPath/Tool/FindTool call, or a
// call to a helper of the same directory that forwards its parameter to
// exec.Command, given an argument naming one; a range over a literal list
// naming one whose body hands the range value to such a call; an exec.Cmd
// literal whose Path or Args names one; and the literal paths of the two
// programs under /usr/bin anywhere. It also returns how many files it read.
func scanForFFmpegCLI(root string) (found []string, checked int) {
	type parsed struct {
		rel  string
		file *ast.File
	}
	byDir := map[string][]parsed{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if ffmpegSkipDir(root, p, d) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
		if err != nil {
			found = append(found, rel+": does not parse: "+err.Error())
			return nil
		}
		byDir[filepath.Dir(p)] = append(byDir[filepath.Dir(p)], parsed{rel, f})
		return nil
	})
	if err != nil {
		found = append(found, "walk: "+err.Error())
	}
	usrBin := []string{"/usr/bin/" + ffmpegNames[0], "/usr/bin/" + ffmpegNames[1]}
	for _, files := range byDir {
		locates := map[string]bool{}
		for name := range ffmpegLocators {
			locates[name] = true
		}
		for _, pf := range files {
			for _, name := range ffmpegForwarders(pf.file) {
				locates[name] = true
			}
		}
		for _, pf := range files {
			checked++
			found = append(found, ffmpegFindings(pf.rel, pf.file, locates, usrBin)...)
		}
	}
	slices.Sort(found)
	return found, checked
}

// ffmpegFindings is scanForFFmpegCLI's verdict on one file, given the
// names that locate a program in its directory.
func ffmpegFindings(rel string, file *ast.File, locates map[string]bool, usrBin []string) []string {
	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				for _, p := range usrBin {
					if strings.Contains(x.Value, p) {
						found = append(found, rel+": names "+p)
					}
				}
			}
		case *ast.CallExpr:
			if name := ffmpegCallee(x); locates[name] && slices.ContainsFunc(x.Args, ffmpegNamedIn) {
				found = append(found, fmt.Sprintf("%s: %s(…) runs or locates an ffmpeg CLI", rel, name))
			}
		case *ast.CompositeLit:
			if sel, ok := x.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Cmd" {
				for _, el := range x.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, _ := kv.Key.(*ast.Ident)
					args, _ := kv.Value.(*ast.CompositeLit)
					if key != nil && (key.Name == "Path" && ffmpegNamedIn(kv.Value) ||
						key.Name == "Args" && args != nil && len(args.Elts) > 0 && ffmpegNamedIn(args.Elts[0])) {
						found = append(found, rel+": an exec.Cmd runs an ffmpeg CLI")
					}
				}
			}
		case *ast.RangeStmt:
			list, ok := x.X.(*ast.CompositeLit)
			v, valueIsIdent := x.Value.(*ast.Ident)
			if !ok || !valueIsIdent || !slices.ContainsFunc(list.Elts, ffmpegNamedIn) {
				return true
			}
			ast.Inspect(x.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if ok && locates[ffmpegCallee(call)] && slices.ContainsFunc(call.Args, func(a ast.Expr) bool { return isIdent(a, v.Name) }) {
					found = append(found, fmt.Sprintf("%s: a range over a list naming an ffmpeg CLI hands it to %s(…)", rel, ffmpegCallee(call)))
				}
				return true
			})
		}
		return true
	})
	return found
}

// No Go file runs the ffmpeg or ffprobe CLI: not production code (the exec
// guard), and not a test, a test helper or a hack/ program either (W10.9,
// upgrade guide U2.4). Tests make media with internal/testmedia and compare
// against recorded goldens.
func TestNoGoFileRunsFFmpeg(t *testing.T) {
	found, checked := scanForFFmpegCLI(installtest.RepoRoot(t))
	for _, f := range found {
		t.Error(f)
	}
	require.Greater(t, checked, 1000, "the walk found too few Go files; it is not looking where it thinks it is")
}

// The guard is falsified on a synthetic tree, each direct shape once (W10.9).
func TestTheFFmpegGuardCatchesEachShape(t *testing.T) {
	root := t.TempDir()
	execGuardWrite(t, root, "a/a_test.go", "package a\nimport \"os/exec\"\nfunc f() { _ = exec.Command(\"ff"+"mpeg\", \"-v\") }\n")
	execGuardWrite(t, root, "b/b.go", "package b\nimport \"os/exec\"\nfunc f() { _, _ = exec.LookPath(\"ff"+"probe\") }\n")
	execGuardWrite(t, root, "test/c/c.go", "package c\nfunc f(t any) { _ = Tool(t, \"ff"+"mpeg\") }\nfunc Tool(any, string) string { return \"\" }\n")
	execGuardWrite(t, root, "hack/d/main.go", "package main\nimport \"os/exec\"\nfunc main() { _ = exec.Command(Tool(nil, \"ff"+"probe\")) }\nfunc Tool(any, string) string { return \"\" }\n")
	execGuardWrite(t, root, "e/e_test.go", "package e\nconst bin = \"/usr/bin/\" + \"x\"\nvar p = \"/usr/bin/ff"+"mpeg\"\n")
	execGuardWrite(t, root, "f/f.go", "package f\nimport \"log/slog\"\nfunc f() { slog.Warn(\"ff"+"mpeg\", \"level\", 1) }\n") // a log message is not a program
	execGuardWrite(t, root, "j/j_test.go", "package j\nimport \"os/exec\"\nfunc f() { _ = exec.Command(\"sh\", \"-c\", \"ff"+"probe -v error x\") }\n")
	execGuardWrite(t, root, "k/k_test.go", "package k\nimport \"os/exec\"\nvar c = exec.Cmd{Path: \"ff"+"mpeg\"}\n")
	found, checked := scanForFFmpegCLI(root)
	got := strings.Join(found, "\n")
	for _, want := range []string{"a/a_test.go", "b/b.go", "test/c/c.go", "hack/d/main.go", "e/e_test.go", "j/j_test.go", "k/k_test.go"} {
		assert.Contains(t, got, want)
	}
	assert.NotContains(t, got, "f/f.go")
	assert.Equal(t, 8, checked)
}

// The indirect shapes the tests used: a helper that forwards its program
// to exec.Command, called from another file of its package
// (pkg/transcode/engine's run), and a gate over a literal list (W10.9).
func TestTheFFmpegGuardCatchesIndirectShapes(t *testing.T) {
	root := t.TempDir()
	execGuardWrite(t, root, "g/run_test.go", "package g\nimport \"os/exec\"\nfunc run(t any, name string, a ...string) { _ = exec.Command(name, a...) }\n")
	execGuardWrite(t, root, "g/use_test.go", "package g\nfunc f() { run(nil, \"ff"+"probe\", \"-v\") }\n")
	execGuardWrite(t, root, "h/h_test.go", "package h\nimport \"os/exec\"\nfunc f() { for _, b := range []string{\"ff"+"mpeg\", \"x\"} { _, _ = exec.LookPath(b) } }\n")
	execGuardWrite(t, root, "i/i_test.go", "package i\nimport \"os\"\nfunc f() { for _, b := range []string{\"ff"+"mpeg\"} { _ = os.WriteFile(b, nil, 0o755) } }\n") // a fake written under the name is not a run
	execGuardWrite(t, root, "l/use_test.go", "package l\nfunc f() { run(nil, \"ff"+"probe\") }\nfunc run(any, string) {}\n")                                         // run forwards nothing here
	found, _ := scanForFFmpegCLI(root)
	got := strings.Join(found, " ")
	assert.Contains(t, got, "g/use_test.go")
	assert.NotContains(t, got, "g/run_test.go", "the forwarder itself names no program")
	assert.Contains(t, got, "h/h_test.go")
	assert.NotContains(t, got, "i/i_test.go")
	assert.NotContains(t, got, "l/use_test.go", "a forwarder counts only in its own directory")
}

// ffmpegGateExempt are the trees whose tests may gate on ffgo themselves:
// the two gates every other test goes through, and the owner-run parity
// suite (behind CLUSTARR_PARITY_DIR).
var ffmpegGateExempt = []string{"internal/testmedia/", "test/nativetest/", "test/parity/"}

// scanForSilentFFgoGates reports every _test.go file under root, outside
// ffmpegGateExempt, with an if whose init or condition calls ffgo.Init or
// ffgo.Version and whose body calls Skip, Skipf or SkipNow: a gate of its
// own that only skips, so a missing library passes silently even under
// CLUSTARR_FFGO_REQUIRE=1. It also returns how many test files it read.
func scanForSilentFFgoGates(root string) (found []string, checked int) {
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if ffmpegSkipDir(root, p, d) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if slices.ContainsFunc(ffmpegGateExempt, func(dir string) bool { return strings.HasPrefix(rel, dir) }) {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			found = append(found, rel+": does not parse: "+err.Error())
			return nil
		}
		checked++
		alias := importAlias(f, "github.com/obinnaokechukwu/ffgo", "ffgo")
		if alias == "" {
			return nil
		}
		callsFFgo := func(n ast.Node) bool {
			hit := false
			ast.Inspect(n, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok && (isSelector(c.Fun, alias, "Init") || isSelector(c.Fun, alias, "Version")) {
					hit = true
				}
				return !hit
			})
			return hit
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ifs, ok := n.(*ast.IfStmt)
			if !ok || !(ifs.Init != nil && callsFFgo(ifs.Init) || callsFFgo(ifs.Cond)) {
				return true
			}
			skips := false
			ast.Inspect(ifs.Body, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if sel, ok := c.Fun.(*ast.SelectorExpr); ok && slices.Contains([]string{"Skip", "Skipf", "SkipNow"}, sel.Sel.Name) {
						skips = true
					}
				}
				return !skips
			})
			if skips {
				found = append(found, fmt.Sprintf("%s:%d: a gate on ffgo that only skips; gate through testmedia.Require or nativetest.FFmpeg",
					rel, fset.Position(ifs.Pos()).Line))
			}
			return true
		})
		return nil
	})
	if err != nil {
		found = append(found, "walk: "+err.Error())
	}
	return found, checked
}

// Every test that needs FFmpeg's libraries gates through testmedia.Require
// or nativetest.FFmpeg, which fail under CLUSTARR_FFGO_REQUIRE=1 (CI): a
// gate of its own that only skips lets a missing library pass silently
// (W10.9, upgrade guide U2.1).
func TestEveryFFgoGateHonoursTheRequirement(t *testing.T) {
	found, checked := scanForSilentFFgoGates(installtest.RepoRoot(t))
	for _, f := range found {
		t.Error(f)
	}
	require.Greater(t, checked, 500, "the walk found too few test files; it is not looking where it thinks it is")
}

// ffmpegScriptRuns matches a shell line that runs ffmpeg or ffprobe: as
// its command, or after a pipe, a list operator or a subshell's paren.
var ffmpegScriptRuns = regexp.MustCompile(`(?m)^\s*([^#\n]*[|;&(]\s*)?ff(mpeg|probe)\s`)

// No shell script under hack/, test/ or images/ runs the ffmpeg or ffprobe
// CLI either (W10.9; hack/parity-clips.sh cut test/parity's clips with one).
func TestNoScriptRunsFFmpeg(t *testing.T) {
	root := installtest.RepoRoot(t)
	scripts := 0
	for _, dir := range []string{"hack", "test", "images"} {
		require.NoError(t, filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".sh") {
				return err
			}
			b, err := os.ReadFile(p)
			require.NoError(t, err)
			scripts++
			rel, _ := filepath.Rel(root, p)
			for _, m := range ffmpegScriptRuns.FindAllString(string(b), -1) {
				t.Errorf("%s runs an ffmpeg CLI: %s", rel, strings.TrimSpace(m))
			}
			return nil
		}))
	}
	require.GreaterOrEqual(t, scripts, 5, "the walk found too few scripts; it is not looking where it thinks it is")
}

// ffmpegNoLineMatches fails once per line of text that pattern matches,
// naming the line rather than dumping the file.
func ffmpegNoLineMatches(t *testing.T, pattern, text, msg string) {
	t.Helper()
	re := regexp.MustCompile(pattern)
	for _, line := range strings.Split(text, "\n") {
		if re.MatchString(line) {
			t.Errorf("%s: %s", msg, strings.TrimSpace(line))
		}
	}
}

// ffmpegReadRepoFile is a repository file's text.
func ffmpegReadRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	require.NoError(t, err)
	return string(b)
}

// The native test assets carry FFmpeg's libraries and no FFmpeg program,
// and every test run requires ffgo, so a test needing FFmpeg can never pass
// by skipping (W10.10, upgrade guide U2.5, U2.6). The par2 CLI may stay
// (test/par2test.CreateSet runs it, OD26).
func TestTheTestEnvironmentHasNoFFmpegProgram(t *testing.T) {
	root := installtest.RepoRoot(t)
	script := ffmpegReadRepoFile(t, root, "hack/native-assets.sh")
	require.Contains(t, script, "FFmpeg-Builds/releases/download", "anchor: native-assets still fetches FFmpeg's libraries")
	ffmpegNoLineMatches(t, `ff/bin/ff(mpeg|probe)`, script, "hack/native-assets.sh installs an FFmpeg program")
	mk := ffmpegReadRepoFile(t, root, "Makefile")
	env := regexp.MustCompile(`(?s)\nNATIVE_ENV = (.*?)\n\n`).FindStringSubmatch(mk)
	require.NotNil(t, env, "the Makefile has no NATIVE_ENV block")
	assert.Contains(t, env[1], "CLUSTARR_FFGO_REQUIRE=1", "make test does not require ffgo")
	ci := ffmpegReadRepoFile(t, root, ".github/workflows/ci.yml")
	assert.Contains(t, ci, `CLUSTARR_FFGO_REQUIRE: "1"`, "CI's tests do not require ffgo")
	ffmpegNoLineMatches(t, `(?m)^[^#\n]*(apt-get install[^\n]*\bff`+`mpeg\b|/ff`+`mpeg-[^/\s]*\.tar|bin/ff(mpeg|probe)\b)`, ci, ".github/workflows/ci.yml installs ffmpeg")
}

// ffmpegPinnedArgs are the native image's FFmpeg pins the e2e fixtures'
// clip stage must share.
var ffmpegPinnedArgs = []string{"FFMPEG_RELEASE", "FFMPEG_VERSION", "FFMPEG_BRANCH", "FFMPEG_SHA256_AMD64", "FFMPEG_SHA256_ARM64"}

// The e2e fixtures image installs no ffmpeg and bakes its clips with
// test/fixtures/mkclips on the native image's pinned FFmpeg (W10.11,
// upgrade guide U2.7).
func TestTheE2EFixturesImageRunsNoFFmpeg(t *testing.T) {
	root := installtest.RepoRoot(t)
	df := ffmpegReadRepoFile(t, root, "images/Dockerfile.e2e-fixtures")
	require.Contains(t, df, "AS build", "anchor: the fixtures image still builds ./test/fixtures")
	ffmpegNoLineMatches(t, `(?m)^[^#\n]*apt-get install[^\n]*\bff`+`mpeg\b`, df, "a fixtures stage installs ffmpeg")
	ffmpegNoLineMatches(t, `(?m)^[^#\n]*(^|RUN|&&|;|\|)\s*ff(mpeg|probe)\s`, df, "a fixtures stage runs the ffmpeg CLI")
	assert.Contains(t, df, "./test/fixtures/mkclips", "the clips are not made by test/fixtures/mkclips")
	native := ffmpegReadRepoFile(t, root, "images/Dockerfile.native")
	for _, arg := range ffmpegPinnedArgs {
		re := regexp.MustCompile(`(?m)^ARG ` + arg + `=(\S+)$`)
		want := re.FindStringSubmatch(native)
		require.NotNil(t, want, "Dockerfile.native has no ARG %s", arg)
		got := re.FindStringSubmatch(df)
		if assert.NotNil(t, got, "Dockerfile.e2e-fixtures has no ARG %s", arg) {
			assert.Equal(t, want[1], got[1], "%s: the fixtures' FFmpeg is the native image's", arg)
		}
	}
}
