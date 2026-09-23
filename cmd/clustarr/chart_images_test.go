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

// imageRef matches a fully-qualified clustarr image path, without its tag.
var imageRef = regexp.MustCompile(`ghcr\.io/mediactl/clustarr(?:/[a-z0-9._-]+)*`)

// TestChartImagesMatchConfig holds the Helm chart's image paths to the ones
// config/ actually deploys.
//
// The two are maintained by hand in different files and nothing connected
// them, so they drifted: config/, the Makefile and images/ all used the
// path-style `ghcr.io/mediactl/clustarr/media`, while the chart asked for
// `ghcr.io/mediactl/clustarr-media`. A chart install would have pulled an
// image that does not exist, and no test in the tree noticed, because
// TestChartAndKustomizeAgreePerComponent compares Deployments per component
// and the media images are used by transcode Jobs and the KEDA ScaledJob
// rather than by any Deployment.
//
// This test compares the SET of image paths rather than matching them up
// component by component, deliberately. The chart and config express the same
// images through different shapes -- the chart splits registry from
// repository and templates the tag, config writes a literal string -- so a
// structural comparison would couple this test to both layouts. What actually
// matters is that neither side names an image the other has never heard of.
func TestChartImagesMatchConfig(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	// The chart's side: registry joined to each repository under `image:`.
	raw, err := os.ReadFile(filepath.Join(root, "charts", "clustarr", "values.yaml"))
	require.NoError(t, err, "read the chart values")

	var values struct {
		Image map[string]any `json:"image"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &values), "parse the chart values")

	registry, _ := values.Image["registry"].(string)
	require.NotEmpty(t, registry, "chart values have no image.registry")

	chart := map[string]string{}
	for key, v := range values.Image {
		entry, ok := v.(map[string]any)
		if !ok {
			continue // registry, pullPolicy, and anything else scalar
		}
		repo, ok := entry["repository"].(string)
		if !ok || repo == "" {
			continue
		}
		chart[registry+"/"+repo] = key
	}
	require.NotEmpty(t, chart, "found no image repositories in the chart values; this test's premise has changed")

	// config's side: every clustarr image path any manifest references.
	deployed := map[string]bool{}
	for _, dir := range []string{"config"} {
		err := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".yaml") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range imageRef.FindAllString(string(b), -1) {
				deployed[m] = true
			}
			return nil
		})
		require.NoError(t, err)
	}
	require.NotEmpty(t, deployed, "found no clustarr image references under config/")

	for path, key := range chart {
		require.True(t, deployed[path],
			"charts/clustarr/values.yaml's image.%s points at %q, which appears nowhere under config/.\n"+
				"The chart and config/ name the same images by hand in different files; when they drift, "+
				"a chart install pulls a tag that was never built. config/ currently deploys:\n  %s",
			key, path, sortedKeys(deployed))
	}
}
