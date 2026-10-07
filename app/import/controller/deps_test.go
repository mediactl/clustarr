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

package controller

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// linked lists what `go list -deps` reports for pkg: what a binary built from
// it links. A _test.go import is not a link, so tests may still import workers.
func linked(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
	require.NoError(t, err, "go list -deps %s:\n%s", pkg, out)
	return strings.Fields(string(out))
}

// denyLinks fails once for every dependency of pkg that is, or is under, one
// of denied.
func denyLinks(t *testing.T, pkg string, denied ...string) {
	t.Helper()
	for _, dep := range linked(t, pkg) {
		for _, bad := range denied {
			bad = strings.TrimSuffix(bad, "/")
			if dep == bad || strings.HasPrefix(dep, bad+"/") {
				assert.Failf(t, "links agent code", "%s links %s", pkg, dep)
			}
		}
	}
}

const importWorkers = "github.com/mediactl/clustarr/app/import/worker"

// TestImportControllersLinkNoWorkerCode holds the manager side of importarr
// to spec §4.3 Wave 2: the reconcilers, and the leaves they read, link no
// app/import/worker package (R6). `test/guards` (W5.14) overlaps it.
func TestImportControllersLinkNoWorkerCode(t *testing.T) {
	require.Contains(t, linked(t, "github.com/mediactl/clustarr/app/import/controller/..."),
		"github.com/mediactl/clustarr/app/import/controller/retrigger",
		"the retrigger reconciler moved under app/import/controller (I5)")
	for _, pkg := range []string{
		"github.com/mediactl/clustarr/app/import/controller/...", // every importarr reconciler, retrigger included
		"github.com/mediactl/clustarr/app/import/scanprogress",
		"github.com/mediactl/clustarr/app/import/mediafilespec",
		"github.com/mediactl/clustarr/app/import/importliststate",
		"github.com/mediactl/clustarr/app/import/importtarget",
	} {
		t.Run(pkg[strings.LastIndex(pkg, "/")+1:], func(t *testing.T) {
			denyLinks(t, pkg, importWorkers)
		})
	}
}
