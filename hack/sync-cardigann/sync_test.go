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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/mediactl/clustarr/pkg/cardigann"
)

// definition is a minimal valid v11 definition with the given id. The
// upstream corpus cannot be a fixture (see the package doc), so the tests
// author their own.
func definition(id string) string {
	return `id: ` + id + `
name: ` + id + `
description: fixture
language: en-US
type: public
encoding: UTF-8
links:
  - https://tracker.example/
caps:
  categorymappings:
    - {id: 1, cat: Movies}
  modes:
    search: [q]
search:
  path: search
  rows:
    selector: tr
  fields:
    title:
      selector: td
    size:
      text: 1
    seeders:
      text: 1
    category:
      text: 1
    download:
      text: "magnet:?xt=urn:btih:abc"
`
}

// corpus is an upstream tree: two good v11 definitions, one the schema
// refuses, one whose id differs from another's only in case, the matching
// schema, and files outside definitions/v11 that must be ignored.
func corpus() map[string]string {
	return map[string]string{
		"definitions/v11/alpha.yml":       definition("alpha"),
		"definitions/v11/beta.yml":        definition("beta"),
		"definitions/v11/broken.yml":      "id: broken\nname: broken\n",
		"definitions/v11/zshouty.yml":     definition("ALPHA"),
		"definitions/v11/schema.json":     string(cardigann.EmbeddedSchema()),
		"definitions/v10/alpha.yml":       definition("old"),
		"definitions/v11/nested/deep.yml": definition("deep"),
		"README.md":                       "not a definition",
	}
}

// tarball is corpus() as GitHub serves it: gzipped tar, every path under
// "<repo>-<commit>/".
func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: "Indexers-" + pinnedCommit + "/" + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func serve(t *testing.T, body []byte) (*httptest.Server, *string) {
	t.Helper()
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &path
}

func TestSyncFetchesThePinnedArchiveAndKeepsWhatTheEngineLoads(t *testing.T) {
	srv, path := serve(t, tarball(t, corpus()))
	out := filepath.Join(t.TempDir(), "defs")

	rep, err := run(options{Commit: pinnedCommit, URL: srv.URL + "/tar.gz/%s", Out: out, Format: formatYAML}, srv.Client())
	require.NoError(t, err)
	assert.Equal(t, "/tar.gz/"+pinnedCommit, *path, "the pinned commit is what is fetched")
	assert.Equal(t, 4, rep.Upstream, "only definitions/v11/*.yml count")
	assert.False(t, rep.SchemaDrift)

	entries, err := os.ReadDir(out)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	// zshouty.yml (id ALPHA) is accepted in yaml format: ids are distinct
	// case-sensitively, and a bundle directory has no object names.
	assert.ElementsMatch(t, []string{"SOURCE.txt", "alpha.yml", "beta.yml", "zshouty.yml"}, names)

	got, err := os.ReadFile(filepath.Join(out, "alpha.yml"))
	require.NoError(t, err)
	assert.Equal(t, definition("alpha"), string(got), "raw upstream bytes, unchanged")

	src, err := os.ReadFile(filepath.Join(out, "SOURCE.txt"))
	require.NoError(t, err)
	assert.Contains(t, string(src), "Prowlarr/Indexers@"+pinnedCommit)
	assert.Contains(t, string(src), "carries no licence")
	assert.Contains(t, string(src), "broken.yml: cardigann: schema validation")

	// The output is a bundle directory LoadBundle takes as it is.
	defs, issues, err := cardigann.LoadBundle(os.DirFS(out))
	require.NoError(t, err)
	assert.Len(t, defs, 3)
	assert.Empty(t, issues)
}

func TestSyncWritesIndexerDefinitionManifests(t *testing.T) {
	srv, _ := serve(t, tarball(t, corpus()))
	out := filepath.Join(t.TempDir(), "crds")

	rep, err := run(options{Commit: pinnedCommit, URL: srv.URL + "/%s", Out: out, Format: formatCRD}, srv.Client())
	require.NoError(t, err)
	require.Len(t, rep.Accepted, 2, "ALPHA would be object name alpha, which alpha.yml already has")

	body, err := os.ReadFile(filepath.Join(out, "alpha.yaml"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(body), "# Prowlarr/Indexers@"+pinnedCommit), "provenance heads the manifest")
	var obj map[string]any
	require.NoError(t, yaml.Unmarshal(body, &obj))
	assert.Equal(t, "index.clustarr.io/v1alpha1", obj["apiVersion"])
	assert.Equal(t, "IndexerDefinition", obj["kind"])
	assert.Equal(t, map[string]any{"name": "alpha"}, obj["metadata"])
	assert.Equal(t, map[string]any{"yaml": definition("alpha")}, obj["spec"])

	src, err := os.ReadFile(filepath.Join(out, "SOURCE.txt"))
	require.NoError(t, err)
	assert.Contains(t, string(src), `zshouty.yml: object name "alpha" already used by alpha.yml`)
}

func TestSyncReadsALocalCheckout(t *testing.T) {
	src := t.TempDir()
	for name, body := range corpus() {
		p := filepath.Join(src, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	rep, err := run(options{Src: src, Out: filepath.Join(t.TempDir(), "o"), Format: formatYAML}, nil)
	require.NoError(t, err)
	assert.Equal(t, 4, rep.Upstream)
	assert.Len(t, rep.Accepted, 3)
}

func TestSyncFlagsSchemaDrift(t *testing.T) {
	files := corpus()
	files["definitions/v11/schema.json"] = `{"changed": true}`
	srv, _ := serve(t, tarball(t, files))
	rep, err := run(options{Commit: pinnedCommit, URL: srv.URL + "/%s", Out: filepath.Join(t.TempDir(), "o"), Format: formatYAML}, srv.Client())
	require.NoError(t, err)
	assert.True(t, rep.SchemaDrift)
	assert.Contains(t, rep.Summary(), "WARNING")
}

func TestSyncRefusesANonEmptyOutputDirectory(t *testing.T) {
	out := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(out, "stale.yml"), []byte("x"), 0o644))
	srv, _ := serve(t, tarball(t, corpus()))
	_, err := run(options{Commit: pinnedCommit, URL: srv.URL + "/%s", Out: out, Format: formatYAML}, srv.Client())
	require.Error(t, err, "a definition upstream removed must not linger from an earlier run")
}

func TestSyncFailsOnAnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, err := run(options{Commit: "nope", URL: srv.URL + "/%s", Out: filepath.Join(t.TempDir(), "o"), Format: formatYAML}, srv.Client())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 404")
}

func TestObjectNameIsADNSSubdomain(t *testing.T) {
	assert.Equal(t, "bittorrentfiles", objectName("Bittorrentfiles"))
	assert.Equal(t, "a-b.c", objectName("a_b.c"))
	assert.Equal(t, "x", objectName("--x--"))
}
