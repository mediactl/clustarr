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
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	importarr "github.com/mediactl/clustarr/app/import"
)

// TestEveryImportarrDeploymentMountsTheDataClaim holds both installers to
// what both importarr roles do on disk. The worker role scans and imports
// library files; the controller role runs the rename controller, which
// moves them (probe-driven naming §5). The controller Deployment shipped
// without /data -- "this Deployment owns only the controllers and does not
// mount /data" -- which no envtest can see: envtest runs no pods, so every
// rename test passed while every rename on a real cluster would have failed
// its first stat.
//
// Each Deployment that runs `clustarr importarr` must mount a
// PersistentVolumeClaim at /data and set UMASK, whatever its --role; both
// readiness gates include /data writability (app/import's Run).
func TestEveryImportarrDeploymentMountsTheDataClaim(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	t.Run("kustomize", func(t *testing.T) { assertKustomizeImportarrData(t, root) })
	t.Run("chart", func(t *testing.T) { assertChartImportarrData(t, root) })
}

// importarrWorkload is the slice of a Deployment manifest this guard reads.
type importarrWorkload struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Args []string `json:"args"`
					Env  []struct {
						Name string `json:"name"`
					} `json:"env"`
					VolumeMounts []struct {
						Name      string `json:"name"`
						MountPath string `json:"mountPath"`
					} `json:"volumeMounts"`
				} `json:"containers"`
				Volumes []struct {
					Name                  string          `json:"name"`
					PersistentVolumeClaim *map[string]any `json:"persistentVolumeClaim"`
				} `json:"volumes"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

func assertKustomizeImportarrData(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "config", "manager")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	roles := map[importarr.Role]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") || entry.Name() == "kustomization.yaml" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)
		for _, doc := range strings.Split(string(raw), "\n---") {
			var w importarrWorkload
			require.NoError(t, yaml.Unmarshal([]byte(doc), &w), "parse %s", entry.Name())
			if w.Kind != "Deployment" {
				continue
			}
			pvcs := map[string]bool{}
			for _, v := range w.Spec.Template.Spec.Volumes {
				if v.PersistentVolumeClaim != nil {
					pvcs[v.Name] = true
				}
			}
			for _, c := range w.Spec.Template.Spec.Containers {
				if len(c.Args) == 0 || c.Args[0] != importarr.ServiceName {
					continue
				}
				roles[roleArg(c.Args)] = true
				mounted := false
				for _, m := range c.VolumeMounts {
					mounted = mounted || (m.MountPath == importarr.DefaultDataPath && pvcs[m.Name])
				}
				require.True(t, mounted, "%s: Deployment %s runs `importarr --role %s` and mounts no "+
					"PersistentVolumeClaim at %s", entry.Name(), w.Metadata.Name, roleArg(c.Args), importarr.DefaultDataPath)
				umask := false
				for _, e := range c.Env {
					umask = umask || e.Name == "UMASK"
				}
				require.True(t, umask, "%s: Deployment %s writes under /data and sets no UMASK (§11)",
					entry.Name(), w.Metadata.Name)
			}
		}
	}
	require.True(t, roles[importarr.RoleController] && roles[importarr.RoleWorker],
		"config/manager should run importarr as both a controller and a worker Deployment; found roles %v", roles)
}

func roleArg(args []string) importarr.Role {
	for i, a := range args {
		if a == "--role" && i+1 < len(args) {
			return importarr.Role(args[i+1])
		}
	}
	return ""
}

var (
	chartWorkloadArgs = regexp.MustCompile(`"args"\s+\(list\s+"importarr"\s+"--role"\s+"([a-z,]+)"\)`)
	chartWorkloadData = regexp.MustCompile(`"data"\s+(true|false)`)
)

// assertChartImportarrData reads deployments.yaml's clustarr.workload calls
// rather than rendering the chart, for the reason
// TestEveryPVCMountingWorkloadGetsFsGroup gives: a guard that shells out to
// helm skips wherever helm is missing. clustarr.workload's "data" flag adds
// the /data claim, its mount, fsGroup and UMASK together
// (TestEveryPVCMountingWorkloadGetsFsGroup holds the fsGroup half).
func assertChartImportarrData(t *testing.T, root string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "charts", "clustarr", "templates", "deployments.yaml"))
	require.NoError(t, err)

	roles := map[importarr.Role]bool{}
	for _, call := range strings.Split(string(raw), `include "clustarr.workload"`)[1:] {
		args := chartWorkloadArgs.FindStringSubmatch(call)
		if args == nil {
			continue
		}
		role := importarr.Role(args[1])
		roles[role] = true
		data := chartWorkloadData.FindStringSubmatch(call)
		require.NotNil(t, data, "the chart's importarr --role %s workload passes no \"data\" flag", role)
		require.Equal(t, "true", data[1], "the chart's importarr --role %s workload does not mount /data", role)
	}
	require.True(t, roles[importarr.RoleController] && roles[importarr.RoleWorker],
		"the chart should run importarr as both a controller and a worker workload; found roles %v", roles)
}
