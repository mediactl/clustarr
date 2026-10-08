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

// The par2 image guards (spec 2026-10-06 §8.9 guards 1 and 2, W9.7 as
// upgrade guide U1 amended it): par2 is a library, par2go's published
// libpar2shim.so, never an executable in an image (ADR-0017).

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/test/installtest"
)

// par2NonComment is text without its comment lines ('#' first), so a
// Dockerfile's prose about what it does not do is not read as doing it.
func par2NonComment(text string) string {
	var keep []string
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}

// par2InstallsExecutable matches a Dockerfile that puts a par2 executable
// in an image: a bin path of one, or par2cmdline-turbo's release zip (the
// CLI's only distribution).
var par2InstallsExecutable = regexp.MustCompile(`(/s?bin/par2\b|par2cmdline-turbo-[^ ]*-linux-[a-z0-9]+\.zip)`)

// §8.9 guard 1: no image ships a par2 executable, stage.sh refuses one, and
// the image checks look for one (W9.7, ADR-0017).
func TestNoImageShipsAPar2Executable(t *testing.T) {
	root := installtest.RepoRoot(t)
	dockerfiles, err := filepath.Glob(filepath.Join(root, "images", "Dockerfile*"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(dockerfiles), 3, "the glob found too few Dockerfiles; it is not looking where it thinks it is")
	for _, f := range dockerfiles {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		if m := par2InstallsExecutable.FindString(string(b)); m != "" {
			t.Errorf("%s installs a par2 executable (%s): par2 is a library (ADR-0017)", filepath.Base(f), m)
		}
	}
	stage := ffmpegReadRepoFile(t, root, "images/distroless/stage.sh")
	assert.Regexp(t, `(?s)for f in [^;\n]*\bpar2\b[^;\n]*; do.*?exit 1`, stage, "stage.sh refuses a staged par2")
	checks := ffmpegReadRepoFile(t, root, "hack/image-checks.sh")
	assert.Regexp(t, `(?s)no-media-executables\)[^;]*-name par2\b`, checks, "the no-media-executables check looks for par2")
}

// §8.9 guard 2: libpar2shim.so comes only from the native image's par2
// stage, which downloads par2go's published release asset for go.mod's
// par2go version and checks it against hack/par2go/SHA256SUMS; nothing
// builds it, and only hack/native-assets.sh fetches a par2 release, the
// test-only CLI (W9.7, upgrade guide U1).
func TestPar2LibraryComesOnlyFromPar2go(t *testing.T) {
	root := installtest.RepoRoot(t)
	df := ffmpegReadRepoFile(t, root, "images/Dockerfile.native")
	code := par2NonComment(df)
	for _, want := range []string{
		"FROM debian:bookworm-slim AS par2",
		"releases/download/${PAR2GO_VERSION}/libpar2shim-linux-${TARGETARCH}.so",
		"COPY hack/par2go/SHA256SUMS",
		"COPY --from=par2 /out/ /",
		"PAR2GO_LIB=/usr/lib/libpar2shim.so",
	} {
		assert.Contains(t, code, want)
	}
	for _, not := range []string{"shim/build.sh", "cmake", "AS par2go", "--from=par2go"} {
		assert.NotContains(t, code, not, "the native image builds the library instead of downloading par2go's")
	}
	assert.Equal(t, 1, strings.Count(code, "/out/usr/lib/libpar2shim.so"), "the library enters the image in one place")

	arg := regexp.MustCompile(`(?m)^ARG PAR2GO_VERSION=(\S+)$`).FindStringSubmatch(df)
	require.NotNil(t, arg, "Dockerfile.native has no ARG PAR2GO_VERSION default")
	mod := strings.TrimSpace(string(installtest.Run(t, root, "go", "list", "-m", "-f", "{{.Version}}", "github.com/mediactl/par2go")))
	require.NotEmpty(t, mod, "go.mod does not require par2go")
	assert.Equal(t, mod, arg[1], "the par2 stage's PAR2GO_VERSION default is go.mod's par2go")
	sums := ffmpegReadRepoFile(t, root, "hack/par2go/SHA256SUMS")
	for _, a := range []string{"amd64", "arm64"} {
		assert.Regexp(t, `(?m)^[0-9a-f]{64}  libpar2shim-linux-`+a+`\.so$`, sums, "SHA256SUMS checks the %s asset", a)
	}

	for _, f := range []string{"Dockerfile.clustarr", "Dockerfile.e2e-fixtures"} {
		assert.NotContains(t, par2NonComment(ffmpegReadRepoFile(t, root, "images/"+f)), "libpar2shim", "%s carries the par2 library", f)
	}
	var fetchers []string
	walked := 0
	for _, dir := range []string{"images", "hack", ".github/workflows"} {
		require.NoError(t, filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			c, err := os.ReadFile(p)
			require.NoError(t, err)
			walked++
			if strings.Contains(string(c), "par2cmdline-turbo/releases/download") {
				rel, _ := filepath.Rel(root, p)
				fetchers = append(fetchers, filepath.ToSlash(rel))
			}
			return nil
		}))
	}
	require.Greater(t, walked, 10, "the walk found too few files; it is not looking where it thinks it is")
	assert.Equal(t, []string{"hack/native-assets.sh"}, fetchers, "only the test assets fetch a par2 release")
}
