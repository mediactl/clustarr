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

package records_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// apiTypes is the one package through which pkg/events/schema names
// Kubernetes types (k8s.io/apimachinery's metav1 and resource): types, not a
// client. Every k8s.io or sigs.k8s.io package pkg/records links must come
// through it.
const apiTypes = "github.com/mediactl/clustarr/api/common/v1alpha1"

// TestRecordsLinksNoKubernetes: the credential-less transcode and markers
// binaries link pkg/records (loop spec §4.2 item 1), so it imports pkg/events,
// its schema and the standard library, and links no Kubernetes client,
// controller-runtime, Prometheus or FFmpeg. (api/common/v1alpha1, which the
// schema names, brings k8s.io/apimachinery types; that is not a client.)
func TestRecordsLinksNoKubernetes(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	require.NoError(t, err)
	for _, imp := range strings.Fields(string(out)) {
		if !strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") {
			continue // the standard library
		}
		assert.Contains(t, []string{"github.com/mediactl/clustarr/pkg/events", "github.com/mediactl/clustarr/pkg/events/schema"},
			imp, "pkg/records imports %s", imp)
	}

	deps := goListDeps(t, ".")
	require.Contains(t, deps, "github.com/mediactl/clustarr/pkg/events", "go list -deps read the package")
	allowed := goListDeps(t, apiTypes)
	for dep := range deps {
		for _, banned := range []string{
			"sigs.k8s.io/controller-runtime", "k8s.io/client-go", "k8s.io/api", "github.com/prometheus/client_golang",
			"go.opentelemetry.io", "github.com/mediactl/ffgo", "github.com/obinnaokechukwu/ffgo", "github.com/ebitengine/purego",
		} {
			if dep == banned || strings.HasPrefix(dep, banned+"/") {
				t.Errorf("pkg/records links %s", dep)
			}
		}
		if (strings.HasPrefix(dep, "k8s.io/") || strings.HasPrefix(dep, "sigs.k8s.io/")) && !allowed[dep] {
			t.Errorf("pkg/records links %s, which %s does not bring", dep, apiTypes)
		}
	}
}

// goListDeps is the transitive dependency set of pkg.
func goListDeps(t *testing.T, pkg string) map[string]bool {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pkg).Output()
	require.NoError(t, err)
	deps := map[string]bool{}
	for _, d := range strings.Fields(string(out)) {
		deps[d] = true
	}
	return deps
}
