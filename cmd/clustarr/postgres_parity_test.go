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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// Task A3 (design spec 2026-09-24 §A.3-A.4; ADR-0010) taught indexarr a
// second release-index engine: a CloudNativePG Postgres Cluster instead of
// the local SQLite volume. Both installers -- charts/clustarr and kustomize
// -- gained a way to opt into it, and TestChartAndKustomizeAgreePerComponent
// (deploy_parity_test.go) only ever compares the SQLite shape (RBAC and
// per-component existence), so a postgres-enabled render could drift between
// the two without either failing. This file is that missing case.

// postgresProbe reads just enough of a manifest document to route it to the
// right typed decode below, and to answer the PVC question without one.
type postgresProbe struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

// secretKeyRef mirrors corev1.SecretKeySelector's two fields this test
// reads.
type secretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// envVar mirrors corev1.EnvVar's two shapes: a literal Value, or a
// ValueFrom.SecretKeyRef.
type envVar struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	ValueFrom *struct {
		SecretKeyRef *secretKeyRef `json:"secretKeyRef"`
	} `json:"valueFrom"`
}

// namedRef is anything with just a .name -- a volume or a volumeMount.
type namedRef struct {
	Name string `json:"name"`
}

// indexarrDeploy is the slice of the indexarr Deployment this file asserts
// on: its update strategy, its volumes, and its one container's env and
// volumeMounts.
type indexarrDeploy struct {
	Spec struct {
		Strategy struct {
			Type string `json:"type"`
		} `json:"strategy"`
		Template struct {
			Spec struct {
				Containers []struct {
					Name         string     `json:"name"`
					Env          []envVar   `json:"env"`
					VolumeMounts []namedRef `json:"volumeMounts"`
				} `json:"containers"`
				Volumes []namedRef `json:"volumes"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

// cnpgCluster is the slice of a CloudNativePG Cluster this file compares
// between installers.
type cnpgCluster struct {
	Spec struct {
		Instances int `json:"instances"`
		Storage   struct {
			Size         string `json:"size"`
			StorageClass string `json:"storageClass"`
		} `json:"storage"`
		Bootstrap struct {
			Initdb struct {
				Database string `json:"database"`
				Owner    string `json:"owner"`
			} `json:"initdb"`
		} `json:"bootstrap"`
	} `json:"spec"`
}

// envByName returns the env entry named name, or nil.
func envByName(env []envVar, name string) *envVar {
	for i := range env {
		if env[i].Name == name {
			return &env[i]
		}
	}
	return nil
}

// hasNamed reports whether refs contains one named name -- a volume or a
// volumeMount.
func hasNamed(refs []namedRef, name string) bool {
	for _, r := range refs {
		if r.Name == name {
			return true
		}
	}
	return false
}

// splitDocs decodes a multi-document YAML stream into raw per-document
// bytes, deferring the typed decode until the caller knows which struct a
// document's kind calls for -- postgresProbe alone cannot hold an
// indexarrDeploy's or a cnpgCluster's fields.
func splitDocs(t *testing.T, in []byte) [][]byte {
	t.Helper()
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(in), 4096)
	var out [][]byte
	for {
		var raw json.RawMessage
		err := dec.Decode(&raw)
		if errors.Is(err, io.EOF) {
			return out
		}
		require.NoError(t, err, "decode rendered manifests")
		if len(bytes.TrimSpace(raw)) == 0 || string(raw) == "null" {
			continue
		}
		out = append(out, append([]byte(nil), raw...))
	}
}

// findDoc locates the one document of the given kind and name, decoded into
// dst, and fails the test if it is missing -- absence here means the
// installer did not render what this test came to compare, not that the
// comparison passed vacuously.
func findDoc(t *testing.T, docs [][]byte, kind, name string, dst any) {
	t.Helper()
	for _, raw := range docs {
		var probe postgresProbe
		require.NoError(t, json.Unmarshal(raw, &probe))
		if probe.Kind != kind || probe.Metadata.Name != name {
			continue
		}
		require.NoError(t, json.Unmarshal(raw, dst))
		return
	}
	t.Fatalf("no %s named %q in this render", kind, name)
}

// hasPVC reports whether any document in docs is a PersistentVolumeClaim
// with the given name.
func hasPVC(docs [][]byte, name string) bool {
	for _, raw := range docs {
		var probe postgresProbe
		if err := json.Unmarshal(raw, &probe); err != nil {
			continue
		}
		if probe.Kind == "PersistentVolumeClaim" && probe.Metadata.Name == name {
			return true
		}
	}
	return false
}

// buildPostgresKustomizeOverlay writes a temporary Kustomization that
// composes config/default with the config/postgres Component, mirroring
// config/README.md's documented wiring: a Component is never built on its
// own, only into an overlay that already has the indexarr Deployment it
// patches. kustomize refuses an absolute path as a resource/component root
// ("new root ... cannot be absolute") regardless of --load-restrictor, so
// the paths must be relative to the temp dir this test writes into.
func buildPostgresKustomizeOverlay(t *testing.T, root string) string {
	t.Helper()
	dir := t.TempDir()
	rel := func(target string) string {
		r, err := filepath.Rel(dir, target)
		require.NoError(t, err)
		return r
	}
	kustomization := "apiVersion: kustomize.config.k8s.io/v1beta1\n" +
		"kind: Kustomization\n" +
		"resources:\n" +
		"- " + rel(filepath.Join(root, "config", "default")) + "\n" +
		"components:\n" +
		"- " + rel(filepath.Join(root, "config", "postgres")) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(kustomization), 0o644))
	return dir
}

// TestChartAndKustomizePostgresAgree is TestChartAndKustomizeAgreePerComponent's
// sibling for the postgres-enabled render (design spec 2026-09-24 §A.3-A.4;
// ADR-0010): both installers must give indexarr the same CLUSTARR_INDEX_DSN
// shape, no index PVC or volume, no Recreate strategy, and the same
// CloudNativePG Cluster spec.
func TestChartAndKustomizePostgresAgree(t *testing.T) {
	helm := findTool(t, "helm")
	kustomize := findTool(t, "kustomize")

	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	chartDocs := splitDocs(t, run(t, root, helm,
		"template", "clustarr", "charts/clustarr",
		"--set", "postgres.enabled=true"))

	overlay := buildPostgresKustomizeOverlay(t, root)
	kzDocs := splitDocs(t, run(t, root, kustomize, "build", overlay))

	var chartDeploy, kzDeploy indexarrDeploy
	findDoc(t, chartDocs, "Deployment", "clustarr-indexarr", &chartDeploy)
	findDoc(t, kzDocs, "Deployment", "indexarr", &kzDeploy)

	var chartCluster, kzCluster cnpgCluster
	findDoc(t, chartDocs, "Cluster", "clustarr-postgres", &chartCluster)
	findDoc(t, kzDocs, "Cluster", "clustarr-postgres", &kzCluster)

	for _, tc := range []struct {
		installer string
		d         indexarrDeploy
	}{
		{"helm", chartDeploy},
		{"kustomize", kzDeploy},
	} {
		t.Run(tc.installer, func(t *testing.T) {
			require.NotEqual(t, "Recreate", tc.d.Spec.Strategy.Type,
				"%s still pins indexarr to Recreate under postgres.enabled; "+
					"only the SQLite shape needs it (§A.3-A.4)", tc.installer)

			require.False(t, hasNamed(tc.d.Spec.Template.Spec.Volumes, "index"),
				"%s still gives indexarr a volume named \"index\" under postgres.enabled; "+
					"there is no PVC to mount once the index lives in Postgres", tc.installer)

			require.Len(t, tc.d.Spec.Template.Spec.Containers, 1,
				"%s: expected exactly one container on the indexarr Deployment", tc.installer)
			container := tc.d.Spec.Template.Spec.Containers[0]

			require.False(t, hasNamed(container.VolumeMounts, "index"),
				"%s still mounts a volume named \"index\" in the indexarr container under "+
					"postgres.enabled", tc.installer)

			dsn := envByName(container.Env, "CLUSTARR_INDEX_DSN")
			require.NotNil(t, dsn, "%s: indexarr has no CLUSTARR_INDEX_DSN env under postgres.enabled", tc.installer)
			require.NotNil(t, dsn.ValueFrom, "%s: CLUSTARR_INDEX_DSN is not valueFrom a Secret", tc.installer)
			require.NotNil(t, dsn.ValueFrom.SecretKeyRef, "%s: CLUSTARR_INDEX_DSN has no secretKeyRef", tc.installer)
			require.Equal(t, "uri", dsn.ValueFrom.SecretKeyRef.Key,
				"%s: CLUSTARR_INDEX_DSN reads the wrong Secret key", tc.installer)
			require.True(t, strings.HasSuffix(dsn.ValueFrom.SecretKeyRef.Name, "-postgres-app"),
				"%s: CLUSTARR_INDEX_DSN Secret %q does not end \"-postgres-app\", "+
					"which is what CNPG's bootstrap.initdb creates for the Cluster's app role",
				tc.installer, dsn.ValueFrom.SecretKeyRef.Name)
		})
	}

	// No index PVC anywhere in either render -- not merely unmounted by the
	// Deployment above, but not provisioned at all.
	require.False(t, hasPVC(chartDocs, "clustarr-index"),
		"helm template --set postgres.enabled=true still renders the clustarr-index PVC")
	require.False(t, hasPVC(kzDocs, "clustarr-index"),
		"the postgres-enabled kustomize overlay still renders the clustarr-index PVC")

	// The two Clusters must agree on the fields both installers set from
	// values/hard-coded YAML respectively: instances, storage size, and the
	// bootstrap database/owner (design spec §A.4). storageClass is left at
	// its empty default by both, so it is compared too.
	require.Equal(t, chartCluster.Spec, kzCluster.Spec,
		"the chart's and kustomize's CloudNativePG Cluster specs disagree:\n  chart:     %+v\n  kustomize: %+v",
		chartCluster.Spec, kzCluster.Spec)
}

// nackScanPaths are the charts/clustarr paths this test reads as Clustarr's
// own source, relative to charts/clustarr. charts/clustarr/charts holds the
// vendored, gitignored dependency tarballs `helm dependency build`
// downloads -- not source, and binary, so scanning it would test what
// CloudNativePG's own chart says about itself rather than what this repo
// wrote.
var nackScanPaths = []string{
	"Chart.yaml", "Chart.lock", "values.yaml", "values.schema.json",
	"README.md", ".gitignore", ".helmignore", "templates", "NOTES.txt",
}

// TestChartHasNoNackReference guards A3's removal of the nack dependency
// (design spec 2026-09-24 §A.5): the chart's own source must never mention
// it again, in Chart.yaml, values.yaml, README.md or any template -- a
// reintroduced `nack:` values block or dependency entry would otherwise pass
// every other test in this file, since none of them assert an installer
// does NOT declare something.
func TestChartHasNoNackReference(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	chartRoot := filepath.Join(root, "charts", "clustarr")

	check := func(p string) {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		require.False(t, strings.Contains(string(b), "nack"),
			"%s still mentions \"nack\" -- A3 removed the nack dependency; "+
				"pkg/events provisions its own JetStream topology and never needed it", p)
	}

	for _, rel := range nackScanPaths {
		path := filepath.Join(chartRoot, rel)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)

		if !info.IsDir() {
			check(path)
			continue
		}
		require.NoError(t, filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			check(p)
			return nil
		}))
	}
}
