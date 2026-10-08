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
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/mediactl/clustarr/ui"
)

// TestUICommandsHandTheChosenAuthModeToRunUI pins §A3.5's explicit
// authentication mode where the process chooses it: `ui --auth-mode` (the
// `clustarr all --ui-auth-mode` leg went with `clustarr all`, §3.10). The
// flag has no default, so a bare command hands its run options whose
// Validate -- the check ui.Run makes before it binds -- refuses to serve and
// says which flag to set.
func TestUICommandsHandTheChosenAuthModeToRunUI(t *testing.T) {
	useUnreachableKubeconfig(t)

	for name, tc := range map[string]struct {
		argv     []string
		wantMode ui.AuthMode
	}{
		"ui --auth-mode anonymous": {
			argv:     []string{"--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous"},
			wantMode: ui.AuthModeAnonymous,
		},
		"ui, no mode": {
			argv: []string{"--bind-address", "127.0.0.1:0"},
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
// that argv runs through the real command to options ui.Run accepts, and a
// mode the binary does not know fails `helm template` at the schema rather
// than crash-looping a pod.
func TestChartUIAuthModeReachesUI(t *testing.T) {
	helm := findTool(t, "helm")
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	useUnreachableKubeconfig(t)

	t.Run("default renders anonymous", func(t *testing.T) {
		out := runTool(t, root, helm, "template", "clustarr", "charts/clustarr")
		dep := uiDeployment(t, deploymentsOf(t, out))
		argv := dep.Spec.Template.Spec.Containers[0].Args
		require.Contains(t, argv, "--auth-mode=anonymous", "argv %v", argv)

		o := captureUIOptions(t, argv...)
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

// findTool resolves name on PATH, then in GOPATH/bin, and skips the test
// when it is absent: a chart cannot be checked without the binary that
// renders it. (test/installtest takes these helpers over in T2, W5.11.)
func findTool(t *testing.T, name string) string {
	t.Helper()
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	if out, err := exec.Command("go", "env", "GOPATH").Output(); err == nil {
		p := filepath.Join(strings.TrimSpace(string(out)), "bin", name)
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	t.Skipf("%s is not installed (PATH or $(go env GOPATH)/bin)", name)
	return ""
}

// runTool executes bin in dir and returns its stdout, failing the test on an
// error or after two minutes.
func runTool(t *testing.T, dir, bin string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Start(), "%s %s", bin, strings.Join(args, " "))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		require.NoError(t, err, "%s %s\n%s", bin, strings.Join(args, " "), stderr.String())
	case <-time.After(2 * time.Minute):
		_ = cmd.Process.Kill()
		t.Fatalf("%s %s: still running after 2m", bin, strings.Join(args, " "))
	}
	return stdout.Bytes()
}

// deploymentsOf decodes the Deployments in a rendered multi-document stream.
func deploymentsOf(t *testing.T, in []byte) []appsv1.Deployment {
	t.Helper()
	var out []appsv1.Deployment
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(in), 4096)
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); errors.Is(err, io.EOF) {
			return out
		} else if err != nil {
			t.Fatalf("decode rendered manifests: %v", err)
		}
		if obj == nil || obj["kind"] != "Deployment" {
			continue
		}
		var d appsv1.Deployment
		require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &d))
		out = append(out, d)
	}
}
