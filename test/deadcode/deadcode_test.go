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

package deadcode

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Go linker drops every method nothing can call -- unless a reachable
// function looks methods up by a name it computes (reflect's MethodByName
// or Method(i)), as text/template's field evaluation does. Then it keeps
// every exported method of every type held in an interface, in the whole
// binary. Two imports did that to every binary (2026-10-07): gRPC's
// golang.org/x/net/trace, which OpenTelemetry's OTLP exporters link and
// whose init keeps html/template reachable, and cobra's SetVersionTemplate.
// The grpcnotrace build tag and cli.NewRoot's own --version removed both:
// ui 56.0 -> 44.5 MB, transcode 27.0 -> 20.7 MB, markers 26.6 -> 21.6 MB.
//
// The manager and the agent still look methods up -- Cardigann evaluates
// definition templates with text/template (the manager logs in to indexers
// from its Indexer controller, the agent searches them), and anacrolix/
// torrent calls go-cmp -- so this holds the three binaries that do not.

// deadcodeBinaries are the binaries whose method dead-code elimination is
// on, built as their images build them.
var deadcodeBinaries = []string{"ui", "markers", "transcode"}

const deadcodeTag = "grpcnotrace"

// Every place a shipped binary is built carries the tag: the Makefile's
// build target and both images' build stages.
func TestEveryShippedBuildCarriesGrpcNoTrace(t *testing.T) {
	root := filepath.Join("..", "..")
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	require.NoError(t, err)
	require.Regexp(t, `(?m)^GOTAGS \?= .*\b`+deadcodeTag+`\b`, string(mk))
	builds := regexp.MustCompile(`(?m)^\t.*go build .*$`).FindAllString(string(mk), -1)
	require.NotEmpty(t, builds, "the Makefile builds the binaries")
	for _, b := range builds {
		assert.Contains(t, b, `-tags "$(GOTAGS)"`, "Makefile: %s", strings.TrimSpace(b))
	}
	for _, f := range []string{"Dockerfile.clustarr", "Dockerfile.native"} {
		df, err := os.ReadFile(filepath.Join(root, "images", f))
		require.NoError(t, err)
		assert.Regexp(t, `GOFLAGS="[^"]*-tags=`+deadcodeTag, string(df), "%s builds with -tags=%s", f, deadcodeTag)
	}
}

// No function that looks methods up by name is reachable in ui, markers
// or transcode. A failure names each one and the chain that reaches it.
func TestTheBinariesKeepMethodDeadCodeElimination(t *testing.T) {
	if testing.Short() {
		t.Skip("builds three binaries")
	}
	root := filepath.Join("..", "..")
	for _, bin := range deadcodeBinaries {
		t.Run(bin, func(t *testing.T) {
			cmd := exec.Command("go", "build", "-tags", deadcodeTag, "-ldflags=-dumpdep", "-o", os.DevNull, "./cmd/"+bin) //nolint:noctx // bounded by the test timeout
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "go build ./cmd/%s:\n%s", bin, tail(out))
			for _, fn := range reflectMethods(string(out)) {
				t.Errorf("%s reaches %s, which looks methods up by name and keeps every exported method:\n  %s",
					bin, fn, strings.Join(chainTo(string(out), fn), "\n  -> "))
			}
		})
	}
}

// reflectMethods are the functions -dumpdep marks <ReflectMethod>.
func reflectMethods(dump string) []string {
	seen := map[string]bool{}
	var fns []string
	for _, m := range regexp.MustCompile(`(?m)^(\S+) <ReflectMethod>`).FindAllStringSubmatch(dump, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			fns = append(fns, m[1])
		}
	}
	return fns
}

// chainTo walks -dumpdep's edges back from target to the program's roots.
func chainTo(dump, target string) []string {
	attr := regexp.MustCompile(` <[^>]*>`)
	callers := map[string][]string{}
	for _, line := range strings.Split(dump, "\n") {
		from, to, ok := strings.Cut(line, " -> ")
		if ok {
			to = attr.ReplaceAllString(to, "")
			callers[to] = append(callers[to], attr.ReplaceAllString(from, ""))
		}
	}
	next := map[string]string{target: ""}
	queue := []string{target}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		if s == "main.main" || s == "go:main.inittasks" {
			var chain []string
			for ; s != ""; s = next[s] {
				chain = append(chain, s)
			}
			return chain
		}
		for _, c := range callers[s] {
			if _, ok := next[c]; !ok {
				next[c] = s
				queue = append(queue, c)
			}
		}
	}
	return []string{target}
}

func tail(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return strings.Join(lines, "\n")
}
