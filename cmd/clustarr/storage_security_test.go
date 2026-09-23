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

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// TestEveryPVCMountingWorkloadGetsFsGroup closes a gap that no existing test
// in this tree could see, in either installer.
//
// fsGroup/fsGroupChangePolicy are what make the kubelet chown a mounted volume
// to the pod's supplemental group. A freshly provisioned PVC root is
// root:root 0755 on most block CSI drivers and every Clustarr pod runs as
// uid 1000, so a PVC-mounting pod WITHOUT fsGroup cannot write to its own
// volume: the process fails on its first write and the pod crash-loops
// forever, with a Deployment that looks perfectly well-formed.
//
// The chart shipped exactly that. clustarr.workload gated the full
// podSecurityContext on `if .data` alone, and indexarr is the only component
// invoked with "data" false and "index" true -- so a Helm-installed indexarr
// got runAsUser: 1000 and no fsGroup, while the kustomize manifest set both.
// It was invisible until Task D1-8 registered the release index, because
// until then nothing opened a file on that volume.
//
// # Why the existing coverage could not see it
//
// TestChartAndKustomizeAgreePerComponent compares which COMPONENTS declare
// which object KINDS; it never looks inside a pod spec. hack/e2e.sh installs
// with kustomize, which is the installer that was already correct, so the e2e
// suite could run green forever. Nothing else renders the chart at all.
//
// # Why this does not shell out to helm
//
// chart_rbac_test.go's reasoning applies here too: a test that ran `helm
// template` would SKIP on any machine without helm, and a skipping guard is a
// guard nobody notices losing. The chart half therefore asserts the invariant
// at its source -- every flag that adds a persistentVolumeClaim volume must
// also appear in the condition that emits fsGroup -- which is exact, needs no
// renderer, and cannot be satisfied by a component-specific patch.
func TestEveryPVCMountingWorkloadGetsFsGroup(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	t.Run("chart", func(t *testing.T) { assertChartFsGroup(t, root) })
	t.Run("kustomize", func(t *testing.T) { assertKustomizeFsGroup(t, root) })
}

var (
	// `{{- if .data }}` -- a condition on a single bare flag.
	tplIfFlag = regexp.MustCompile(`^\{\{-?\s*if\s+\.([A-Za-z_][A-Za-z0-9_]*)\s*-?\}\}$`)
	// `{{- if or .data .index }}` -- any condition at all.
	tplIfAny = regexp.MustCompile(`^\{\{-?\s*if\s+(.+?)\s*-?\}\}$`)
)

// assertChartFsGroup discovers, from charts/clustarr/templates/_helpers.tpl
// itself, which flags add a PVC and which condition emits fsGroup, then
// requires the second to cover the first. Nothing here is a hand-maintained
// list: a third volume flag added tomorrow is discovered and enforced.
func assertChartFsGroup(t *testing.T, root string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "charts", "clustarr", "templates", "_helpers.tpl"))
	require.NoError(t, err)
	lines := strings.Split(string(raw), "\n")

	// Every flag whose block adds a persistentVolumeClaim volume.
	var pvcFlags []string
	inVolumes, current := false, ""
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		switch {
		case trimmed == "volumes:":
			inVolumes = true
		case !inVolumes:
		case tplIfFlag.MatchString(trimmed):
			current = tplIfFlag.FindStringSubmatch(trimmed)[1]
		case strings.HasPrefix(trimmed, "{{- end }}"):
			current = ""
		case strings.HasPrefix(trimmed, "persistentVolumeClaim:") && current != "":
			pvcFlags = append(pvcFlags, current)
		}
	}
	require.NotEmpty(t, pvcFlags,
		"no persistentVolumeClaim volume was found behind a flag in _helpers.tpl; this guard is "+
			"not looking where it thinks it is (was the volumes: block restructured?)")

	// The condition guarding the branch that emits the FULL podSecurityContext
	// -- the one that keeps fsGroup, as opposed to the `omit` branch.
	fsGroupCond, sawOmit := "", false
	for i, ln := range lines {
		if !strings.Contains(ln, "$root.Values.podSecurityContext") {
			continue
		}
		if strings.Contains(ln, "omit") {
			sawOmit = strings.Contains(ln, `"fsGroup"`)
			continue
		}
		for j := i - 1; j >= 0 && j > i-4; j-- {
			if m := tplIfAny.FindStringSubmatch(strings.TrimSpace(lines[j])); m != nil {
				fsGroupCond = m[1]
				break
			}
		}
	}
	require.NotEmpty(t, fsGroupCond,
		"could not find the condition guarding the full podSecurityContext in _helpers.tpl")
	require.True(t, sawOmit,
		"_helpers.tpl no longer has an `omit ... \"fsGroup\"` branch, so this guard is asserting "+
			"against a structure that has changed")

	for _, flag := range pvcFlags {
		require.Contains(t, fsGroupCond, "."+flag,
			"clustarr.workload adds a persistentVolumeClaim when .%s is set, but the condition "+
				"that emits fsGroup/fsGroupChangePolicy is `%s`, which does not mention it. A "+
				"component invoked with %q true gets a PVC and no fsGroup: the kubelet never "+
				"chowns the volume, uid 1000 cannot write to a root:root 0755 PVC root, and the "+
				"pod crash-loops forever. This is how Helm-installed indexarr shipped.",
			flag, fsGroupCond, flag)
	}
}

// pvcWorkload is the slice of a workload manifest this guard reads.
type pvcWorkload struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				SecurityContext struct {
					FSGroup             *int64 `json:"fsGroup"`
					FSGroupChangePolicy string `json:"fsGroupChangePolicy"`
				} `json:"securityContext"`
				Volumes []struct {
					Name                  string          `json:"name"`
					PersistentVolumeClaim *map[string]any `json:"persistentVolumeClaim"`
				} `json:"volumes"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

// assertKustomizeFsGroup is the other installer, and it is plain YAML: every
// workload in config/manager that mounts a PVC must set fsGroup.
func assertKustomizeFsGroup(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "config", "manager")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var checked int
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") || entry.Name() == "kustomization.yaml" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)

		for _, doc := range strings.Split(string(raw), "\n---") {
			if strings.TrimSpace(doc) == "" {
				continue
			}
			var w pvcWorkload
			require.NoError(t, yaml.Unmarshal([]byte(doc), &w), "parse %s", entry.Name())

			var mounted []string
			for _, v := range w.Spec.Template.Spec.Volumes {
				if v.PersistentVolumeClaim != nil {
					mounted = append(mounted, v.Name)
				}
			}
			if len(mounted) == 0 {
				continue
			}
			checked++
			require.NotNil(t, w.Spec.Template.Spec.SecurityContext.FSGroup,
				"%s: %s %s mounts the PVC(s) %v and sets no fsGroup. The kubelet then never "+
					"chowns the volume, and a pod running as a non-root uid cannot write to a "+
					"freshly provisioned root:root PVC root -- it crash-loops forever.",
				entry.Name(), w.Kind, w.Metadata.Name, mounted)
		}
	}
	require.Positive(t, checked,
		"no PVC-mounting workload was found under config/manager; this guard is not looking "+
			"where it thinks it is")
}
