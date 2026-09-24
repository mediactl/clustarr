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
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// TestExternalURLFlagReachesTheChart holds cmd/clustarr/flags.go's own
// claim -- that the chart passes ui's --external-url through
// `ui.plex.externalURL` -- to the chart actually doing it, the same way
// TestChartUIAuthModeReachesUI holds --auth-mode to `ui.auth.mode`.
//
// Task D1 (this task) added the flag and the comment naming the mapping,
// but does not own charts/** -- Task D2 is adding `ui.plex.externalURL`
// (and `ui.plex.enabled` for --plex-provider) to
// charts/clustarr/templates/deployments.yaml and values.yaml. D2 may land
// after D1, so this test first checks whether values.yaml has a `ui.plex`
// key at all: while it does not, it skips with a message saying so, rather
// than failing on infrastructure this task does not own. Once D2 lands,
// this test starts running for real and would fail loudly the day the
// chart and the flags.go comment drift apart again -- which is the whole
// point of adding it now rather than waiting for that day.
func TestExternalURLFlagReachesTheChart(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(root, "charts", "clustarr", "values.yaml"))
	require.NoError(t, err, "read the chart values")

	var values map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &values), "parse the chart values")

	ui, _ := values["ui"].(map[string]any)
	if _, ok := ui["plex"]; !ok {
		t.Skip("charts/clustarr/values.yaml has no ui.plex key yet (Task D2 adds it); " +
			"this test starts asserting for real once that lands")
	}

	helm := findTool(t, "helm")
	const wantExternalURL = "https://x.example"
	out := run(t, root, helm, "template", "clustarr", "charts/clustarr",
		"--set", "ui.plex.externalURL="+wantExternalURL)

	dep := uiDeployment(t, decodeRendered(t, out).deployments)
	argv := dep.Spec.Template.Spec.Containers[0].Args
	require.Contains(t, argv, "--external-url="+wantExternalURL, "argv %v", argv)
}
