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
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/mediactl/clustarr/test/installtest"
	"github.com/mediactl/clustarr/ui"
)

// TestChartUIAuthModeReachesUI holds the chart to §A3.5's explicit mode: its
// default `ui.auth.mode: anonymous` renders the flag on the ui Deployment,
// that argv runs through the real command to options ui.Run accepts, and a
// mode the binary does not know fails `helm template` at the schema rather
// than crash-looping a pod.
func TestChartUIAuthModeReachesUI(t *testing.T) {
	useUnreachableKubeconfig(t)

	t.Run("default renders anonymous", func(t *testing.T) {
		argv := uiArgs(t, installtest.RenderChart(t))
		require.Contains(t, argv, "--auth-mode=anonymous", "argv %v", argv)

		o := captureUIOptions(t, argv...)
		require.Equal(t, ui.AuthModeAnonymous, o.AuthMode)
		require.NoError(t, o.Validate())
	})

	t.Run("an unknown mode is refused at render", func(t *testing.T) {
		cmd := exec.Command(installtest.FindTool(t, "helm"), "template", "clustarr", "charts/clustarr",
			"--set", "ui.auth.mode=basic")
		cmd.Dir = installtest.RepoRoot(t)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		require.Error(t, cmd.Run(), "helm template must refuse ui.auth.mode=basic")
		require.True(t, strings.Contains(stderr.String(), "ui.auth.mode") || strings.Contains(stderr.String(), "/ui/auth/mode"),
			"the refusal must name the value: %s", stderr.String())
	})
}

// TestExternalURLReachesTheUI holds the chart's `ui.plex.externalURL` to
// the --external-url the Plex provider builds every thumb, art and image URL
// on (design spec §D.1): the rendered ui container's argv, executed through
// the real command, hands ui.Run that URL. Executing the argv, rather than
// finding the flag in it, also proves the container's argv parses as cmd/ui
// does -- no subcommand token, no flag it does not know.
func TestExternalURLReachesTheUI(t *testing.T) {
	useUnreachableKubeconfig(t)
	const want = "https://x.example"

	o := captureUIOptions(t, uiArgs(t, installtest.RenderChart(t, "--set", "ui.plex.externalURL="+want))...)
	require.NotNil(t, o.Plex, "the chart enables the Plex provider by default")
	require.Equal(t, want, o.Plex.ExternalURL)

	o = captureUIOptions(t, uiArgs(t, installtest.RenderChart(t))...)
	require.NotNil(t, o.Plex)
	require.Empty(t, o.Plex.ExternalURL, "no ui.plex.externalURL: the provider answers 503 until one is set")
}

// uiArgs is the ui container's argv from a rendered chart, as cmd/ui parses
// it.
func uiArgs(t *testing.T, rendered []byte) []string {
	t.Helper()
	c := uiContainer(t, uiDeployment(t, installtest.DecodeRendered(t, rendered).Deployments))
	require.Equal(t, []string{"/usr/bin/ui"}, c.Command, "the ui container runs the ui binary")
	return c.Args
}

// uiDeployment finds the ui Deployment among the rendered ones.
func uiDeployment(t *testing.T, deps []appsv1.Deployment) *appsv1.Deployment {
	t.Helper()
	for i := range deps {
		if deps[i].Spec.Template.Labels["app.kubernetes.io/component"] == "ui" {
			return &deps[i]
		}
	}
	t.Fatal("no ui Deployment rendered")
	return nil
}

// uiContainer is the ui Deployment's container named ui.
func uiContainer(t *testing.T, d *appsv1.Deployment) corev1.Container {
	t.Helper()
	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == "ui" {
			return c
		}
	}
	t.Fatalf("Deployment %s has no container named ui", d.Name)
	return corev1.Container{}
}
