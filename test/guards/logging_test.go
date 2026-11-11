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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/test/installtest"
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

// subsystemPrefixSites are the files that name a subsystem from a prefix
// and a runtime name rather than a literal, with the prefix each is allowed:
// the remediation loop runs its planners and item stages by name.
var subsystemPrefixSites = map[string]string{
	"app/remediation/planner.go":   "remediation.",
	"app/remediation/itemstage.go": "remediation.",
}

// subsystemTableSites are the files that name a subsystem per entry of a
// table of literals in the same file, with the prefix those literals carry:
// the import agent's consumers.
var subsystemTableSites = map[string]string{
	"app/import/agent/register.go": "import.",
}

// TestEverySubsystemIsInTheTable (design §5, §9): every logging.Subsystem and
// SubsystemLogger call names its subsystem with a string literal (or a
// package's own `const Subsystem`, or a "remediation."+name concatenation
// where subsystemPrefixSites allows it); every such name is in
// logging.Subsystems; every entry of the table is used; and every Reconcile
// method taking a context names one.
func TestEverySubsystemIsInTheTable(t *testing.T) {
	table := map[string]bool{}
	for i, name := range logging.Subsystems {
		assert.False(t, table[name], "logging.Subsystems lists %q twice", name)
		table[name] = true
		if i > 0 {
			assert.Less(t, logging.Subsystems[i-1], name, "logging.Subsystems is sorted")
		}
	}
	used := map[string]bool{}
	var problems []string
	for path, pf := range productionFilesWithPositions(t) {
		alias := importAlias(pf.file, "github.com/mediactl/clustarr/pkg/obs/logging", "logging")
		if pf.file.Name.Name == "logging" && strings.HasPrefix(path, "pkg/obs/logging/") {
			alias = ""
		}
		consts := map[string]string{}
		ast.Inspect(pf.file, func(n ast.Node) bool {
			gen, ok := n.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				return true
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if i < len(vs.Values) {
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							consts[id.Name] = strings.Trim(lit.Value, `"`)
						}
					}
				}
			}
			return false
		})
		ast.Inspect(pf.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var nameArg ast.Expr
			switch {
			case alias != "" && (isSelector(call.Fun, alias, "Subsystem") || isSelector(call.Fun, alias, "SubsystemLogger")),
				alias == "" && strings.HasPrefix(path, "pkg/obs/logging/") && (isIdent(call.Fun, "Subsystem") || isIdent(call.Fun, "SubsystemLogger")):
				if len(call.Args) == 2 {
					nameArg = call.Args[1]
				}
			default:
				return true
			}
			where := fmt.Sprintf("%s:%d", path, pf.fset.Position(call.Pos()).Line)
			switch v := nameArg.(type) {
			case *ast.BasicLit:
				name := strings.Trim(v.Value, `"`)
				used[name] = true
				if !table[name] {
					problems = append(problems, where+": subsystem "+name+" is not in logging.Subsystems")
				}
			case *ast.Ident:
				if strings.HasPrefix(path, "pkg/obs/logging/") {
					return true // the package's own plumbing passes the name through
				}
				if prefix, ok := subsystemTableSites[path]; ok {
					// The file's consumer table names each consumer's
					// subsystem as the third element of its row, a literal
					// with the prefix.
					ast.Inspect(pf.file, func(n ast.Node) bool {
						row, ok := n.(*ast.CompositeLit)
						if !ok || len(row.Elts) != 3 {
							return true
						}
						lit, ok := row.Elts[2].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							return true
						}
						name := strings.Trim(lit.Value, `"`)
						if !strings.HasPrefix(name, prefix) {
							problems = append(problems, where+": consumer subsystem "+name+" lacks the prefix "+prefix)
							return true
						}
						used[name] = true
						if !table[name] {
							problems = append(problems, where+": subsystem "+name+" is not in logging.Subsystems")
						}
						return true
					})
					return true
				}
				name, ok := consts[v.Name]
				if !ok {
					problems = append(problems, where+": the subsystem is not a literal or this file's const")
					return true
				}
				used[name] = true
				if !table[name] {
					problems = append(problems, where+": subsystem "+name+" is not in logging.Subsystems")
				}
			case *ast.BinaryExpr:
				lit, ok := v.X.(*ast.BasicLit)
				prefix := subsystemPrefixSites[path]
				if !ok || v.Op != token.ADD || prefix == "" || strings.Trim(lit.Value, `"`) != prefix {
					problems = append(problems, where+": the subsystem is built from something other than this file's allowed prefix")
					return true
				}
				for name := range table {
					if strings.HasPrefix(name, prefix) {
						used[name] = true
					}
				}
			default:
				problems = append(problems, where+": the subsystem is not a literal")
			}
			return true
		})
		for _, decl := range pf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name.Name != "Reconcile" || fn.Body == nil || len(fn.Type.Params.List) == 0 || !isContextParam(fn.Type.Params.List[0].Type) {
				continue
			}
			if names := fn.Type.Params.List[0].Names; len(names) == 1 && names[0].Name == "_" {
				continue // a harness that ignores its context names nothing
			}
			names := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok && alias != "" && isSelector(call.Fun, alias, "Subsystem") {
					names = true
				}
				return !names
			})
			if !names {
				problems = append(problems, fmt.Sprintf("%s:%d: Reconcile names no subsystem", path, pf.fset.Position(fn.Pos()).Line))
			}
		}
	}
	for name := range table {
		if !used[name] {
			problems = append(problems, "logging.Subsystems lists "+name+", which nothing names")
		}
	}
	sort.Strings(problems)
	assert.Empty(t, problems, strings.Join(problems, "\n"))
}

// isContextParam reports whether e is the type expression context.Context.
func isContextParam(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "context" && sel.Sel.Name == "Context"
}
