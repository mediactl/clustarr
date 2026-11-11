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

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mediactl/clustarr/test/installtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The guards of the 2026-11-10 structured-logging design (§9): one error
// key, no global logger, no stderr print in a running process.

// errorKeySites lists every string literal "error" at a key position of a
// log call in file: the key/value pairs of Debug, Info, Warn, Error and
// logging.With (from the second argument), of their Context forms (from the
// third), of Log (from the fourth), and the key of a slog attribute
// constructor. The key is err (design §3).
func errorKeySites(path string, file *ast.File, fset *token.FileSet) []string {
	var sites []string
	keysFrom := func(args []ast.Expr, first int) {
		for i := first; i < len(args); i += 2 {
			if lit, ok := args[i].(*ast.BasicLit); ok && lit.Kind == token.STRING && lit.Value == `"error"` {
				sites = append(sites, fmt.Sprintf("%s:%d", path, fset.Position(lit.Pos()).Line))
			}
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == "slog" {
			switch sel.Sel.Name {
			case "String", "Any", "Int", "Int64", "Uint64", "Float64", "Bool", "Duration", "Time", "Group":
				keysFrom(call.Args, 0)
			}
			return true
		}
		switch sel.Sel.Name {
		case "Debug", "Info", "Warn", "Error", "With":
			keysFrom(call.Args, 1)
		case "DebugContext", "InfoContext", "WarnContext", "ErrorContext":
			keysFrom(call.Args, 2)
		case "Log":
			keysFrom(call.Args, 3)
		}
		return true
	})
	return sites
}

func TestNoErrorAttributeKey(t *testing.T) {
	var sites []string
	for path, file := range productionFilesWithPositions(t) {
		sites = append(sites, errorKeySites(path, file.file, file.fset)...)
	}
	sort.Strings(sites)
	assert.Empty(t, sites, "a log attribute key \"error\": the one error key is err (design §3)\n%s", strings.Join(sites, "\n"))
}

// TestNoGlobalLogger: slog.Default, slog.New, slog.SetDefault and slog's
// handler constructors appear only in pkg/obs/logging, which builds the one
// root logger a process runs with (design §4, §5). Every other package
// takes the logger from its context or is handed one.
func TestNoGlobalLogger(t *testing.T) {
	var sites []string
	for path, file := range productionFilesWithPositions(t) {
		if strings.HasPrefix(path, "pkg/obs/logging/") {
			continue
		}
		ast.Inspect(file.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			for _, name := range []string{"Default", "New", "SetDefault", "NewJSONHandler", "NewTextHandler"} {
				if isSelector(call.Fun, "slog", name) {
					sites = append(sites, fmt.Sprintf("%s:%d slog.%s", path, file.fset.Position(call.Pos()).Line, name))
				}
			}
			return true
		})
	}
	sort.Strings(sites)
	assert.Empty(t, sites, "a global or hand-built slog logger outside pkg/obs/logging\n%s", strings.Join(sites, "\n"))
}

// stderrExempt names the production files that may print to stderr or
// through the log package, each with its reason: a process's error before
// its logger exists, or a child whose stderr its parent reads.
var stderrExempt = map[string]string{
	"internal/cli/main.go":   "the CLI's one error print, before a logger exists",
	"cmd/markers/main.go":    "a flag or environment error before the logger exists",
	"cmd/transcode/main.go":  "a flag or environment error before the logger exists",
	"pkg/par2child/child.go": "the par2 child process, whose stderr its parent reads as the repair's output",
}

// TestNoStdlibLogOrStderrPrints: no log.Print*, log.Fatal* or log.Panic*,
// and no fmt.Fprint*(os.Stderr, ...) in production code (design §9);
// everything a running process says goes through its logger.
func TestNoStdlibLogOrStderrPrints(t *testing.T) {
	var sites []string
	for path, file := range productionFilesWithPositions(t) {
		if _, ok := stderrExempt[path]; ok {
			continue
		}
		logAlias := importAlias(file.file, "log", "log")
		ast.Inspect(file.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			line := file.fset.Position(call.Pos()).Line
			if logAlias != "" {
				for _, name := range []string{"Print", "Printf", "Println", "Fatal", "Fatalf", "Fatalln", "Panic", "Panicf", "Panicln"} {
					if isSelector(call.Fun, logAlias, name) {
						sites = append(sites, fmt.Sprintf("%s:%d log.%s", path, line, name))
					}
				}
			}
			for _, name := range []string{"Fprint", "Fprintf", "Fprintln"} {
				if isSelector(call.Fun, "fmt", name) && len(call.Args) > 0 && isSelector(call.Args[0], "os", "Stderr") {
					sites = append(sites, fmt.Sprintf("%s:%d fmt.%s(os.Stderr", path, line, name))
				}
			}
			return true
		})
	}
	sort.Strings(sites)
	assert.Empty(t, sites, "a print past the logger\n%s", strings.Join(sites, "\n"))
	for path, why := range stderrExempt {
		assert.NotEmpty(t, why, path)
	}
}

// parsedFile is a production file with the FileSet its positions resolve
// through, for a guard that reports lines.
type parsedFile struct {
	file *ast.File
	fset *token.FileSet
}

// productionFilesWithPositions is productionFiles keeping each file's
// FileSet.
func productionFilesWithPositions(t *testing.T) map[string]parsedFile {
	t.Helper()
	root := installtest.RepoRoot(t)
	out := map[string]parsedFile{}
	for _, dir := range []string{"app", "pkg", "internal", "cmd", "ui"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if perr != nil {
				return perr
			}
			rel, _ := filepath.Rel(root, path)
			out[filepath.ToSlash(rel)] = parsedFile{file: file, fset: fset}
			return nil
		})
		require.NoError(t, err)
	}
	return out
}
