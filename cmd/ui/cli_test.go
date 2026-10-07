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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"

	"github.com/mediactl/clustarr/ui"
)

// TestUICommandsHandTheChosenAuthModeToRunUI pins §A3.5's explicit
// authentication mode at the two places a process chooses it: `clustarr ui
// --auth-mode` and `clustarr all --ui-auth-mode`. Neither flag has a
// default, so a bare command hands runUI options whose Validate -- the
// check ui.Run makes before it binds -- refuses to serve and says which flag
// to set. Both commands build ui.Options separately, hence both cases.
func TestUICommandsHandTheChosenAuthModeToRunUI(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfig, []byte(unreachableKubeconfig), 0o600))
	t.Setenv("KUBECONFIG", kubeconfig)

	for name, tc := range map[string]struct {
		argv     []string
		wantMode ui.AuthMode
	}{
		"clustarr ui --auth-mode anonymous": {
			argv:     []string{"ui", "--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous"},
			wantMode: ui.AuthModeAnonymous,
		},
		"clustarr all --ui-auth-mode anonymous": {
			argv:     []string{"all", "--ui-auth-mode", "anonymous"},
			wantMode: ui.AuthModeAnonymous,
		},
		"clustarr ui, no mode": {
			argv: []string{"ui", "--bind-address", "127.0.0.1:0"},
		},
		"clustarr all, no mode": {
			argv: []string{"all"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			o := captureUIOptions(t, tc.argv...)
			require.Equal(t, tc.wantMode, o.AuthMode)
			if tc.wantMode == "" {
				err := o.Validate()
				require.Error(t, err, "a ui started without an authentication mode must refuse to serve")
				require.Contains(t, err.Error(), "--auth-mode")
				return
			}
			require.NoError(t, o.Validate())
		})
	}
}

// TestChartUIAuthModeReachesUI holds the chart to the same rule: its
// default `ui.auth.mode: anonymous` renders the flag on the ui Deployment,
// that argv runs through the real command tree to options ui.Run accepts,
// and a mode the binary does not know fails `helm template` at the schema
// rather than crash-looping a pod.
func TestChartUIAuthModeReachesUI(t *testing.T) {
	helm := findTool(t, "helm")
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	t.Run("default renders anonymous", func(t *testing.T) {
		out := run(t, root, helm, "template", "clustarr", "charts/clustarr")
		dep := uiDeployment(t, decodeRendered(t, out).deployments)
		argv := dep.Spec.Template.Spec.Containers[0].Args
		require.Contains(t, argv, "--auth-mode=anonymous", "argv %v", argv)

		got := stubEveryService(t)
		if out, err := execute(t, argv...); err != nil {
			t.Fatalf("clustarr %v: %v (output %q)", argv, err, out)
		}
		require.NotNil(t, *got, "clustarr %v ran no service", argv)
		o, ok := (*got).(ui.Options)
		require.True(t, ok, "clustarr %v ran %T, want ui", argv, *got)
		require.Equal(t, ui.AuthModeAnonymous, o.AuthMode)
		require.NoError(t, o.Validate())
	})

	t.Run("an unknown mode is refused at render", func(t *testing.T) {
		cmd := exec.Command(helm, "template", "clustarr", "charts/clustarr", "--set", "ui.auth.mode=basic")
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		require.Error(t, err, "helm template must refuse ui.auth.mode=basic")
		require.True(t, strings.Contains(stderr.String(), "ui.auth.mode") || strings.Contains(stderr.String(), "/ui/auth/mode"),
			"the refusal must name the value: %s", stderr.String())
	})
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
