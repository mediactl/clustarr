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
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/mediactl/clustarr/internal/cli/manager"
	"github.com/mediactl/clustarr/pkg/binpath"
	"github.com/mediactl/clustarr/test/installtest"
)

// renderedContainer is the container named name in the one Deployment that
// runs it.
func renderedContainer(t *testing.T, r installtest.Rendered, name string) corev1.Container {
	t.Helper()
	var found []corev1.Container
	for _, d := range r.Deployments {
		for _, c := range d.Spec.Template.Spec.Containers {
			if c.Name == name {
				found = append(found, c)
			}
		}
	}
	require.Len(t, found, 1, "containers named %s", name)
	return found[0]
}

// setEnvFrom gives this process the container's rendered environment, as the
// kubelet would: literal values, and the downward API's pod name and
// namespace. A variable the container does not set is cleared, so the test
// sees only what the installer rendered.
func setEnvFrom(t *testing.T, c corev1.Container, pod string) {
	t.Helper()
	for _, env := range installerEnv {
		t.Setenv(env, "")
	}
	for _, e := range c.Env {
		switch {
		case e.ValueFrom == nil:
			t.Setenv(e.Name, e.Value)
		case e.ValueFrom.FieldRef != nil && e.ValueFrom.FieldRef.FieldPath == "metadata.name":
			t.Setenv(e.Name, pod)
		case e.ValueFrom.FieldRef != nil && e.ValueFrom.FieldRef.FieldPath == "metadata.namespace":
			t.Setenv(e.Name, "clustarr-system")
		}
	}
}

// TestInstallerArgsParseAsTheManager: the manager container each installer
// renders runs through this binary's own command tree with its rendered
// environment and yields valid options under the one lease. envtest runs no
// pods, so this is where an unknown flag or env var shows up first.
func TestInstallerArgsParseAsTheManager(t *testing.T) {
	for name, tc := range map[string]struct {
		out       func() []byte
		autoscale bool
		defsDir   string
	}{
		"helm":                  {out: func() []byte { return installtest.RenderChart(t) }, autoscale: true},
		"helm, autoscaling off": {out: func() []byte { return installtest.RenderChart(t, "--set", "autoscaling.enabled=false") }},
		"helm, cardigann definitions": {out: func() []byte {
			return installtest.RenderChart(t, "--set", "manager.cardigann.definitions.configMap=defs",
				"--set", "manager.cardigann.bundled=false")
		}, autoscale: true, defsDir: "/etc/clustarr/cardigann"},
		"kustomize": {out: func() []byte { return installtest.BuildKustomize(t, "config/default") }, autoscale: true},
	} {
		t.Run(name, func(t *testing.T) {
			c := renderedContainer(t, installtest.DecodeRendered(t, tc.out()), "manager")
			require.Equal(t, []string{binpath.Manager}, c.Command)
			setEnvFrom(t, c, "manager-0")
			var got manager.Options
			ran := false
			cmd := manager.NewCommandWith(func(_ context.Context, o manager.Options) error { got, ran = o, true; return nil })
			cmd.SetArgs(c.Args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			require.NoError(t, cmd.Execute(), "manager %v", c.Args)
			require.True(t, ran)
			require.NoError(t, got.Validate())
			assert.True(t, got.LeaderElect, "the manager runs under the lease")
			assert.Equal(t, "clustarr-system", got.Namespace)
			assert.Equal(t, tc.autoscale, got.Autoscale)
			assert.NotEmpty(t, got.NativeImage)
			assert.Equal(t, tc.defsDir, got.CardigannDefinitionsDir)
			assert.Equal(t, tc.defsDir == "", got.CardigannBundled)
		})
	}
}

// TestManagerCarriesTheFoldFlags (loop spec §3.7, §3.17, §5.13): both
// installers run the manager with the remediation loop's concurrency, write
// pacing and I/O workers and the admission ledger's graft slots, and never
// --job-retention, which the fold removed with TranscodeJob (D5). The
// cold-turkey revision dropped the --legacy-fold flags: none is rendered.
func TestManagerCarriesTheFoldFlags(t *testing.T) {
	want := []string{
		"--graft-concurrency=2",
		"--remediation-concurrency=16",
		"--remediation-bulk-writes-per-second=25",
		"--remediation-io-workers=8",
	}
	for name, out := range map[string]func() []byte{
		"helm":      func() []byte { return installtest.RenderChart(t) },
		"kustomize": func() []byte { return installtest.BuildKustomize(t, "config/default") },
	} {
		args := renderedContainer(t, installtest.DecodeRendered(t, out()), "manager").Args
		for _, a := range want {
			assert.Contains(t, args, a, "%s's manager args", name)
		}
		for _, a := range args {
			assert.False(t, strings.HasPrefix(a, "--job-retention"), "%s's manager passes %s: ADR-0016 removed it (loop spec D5)", name, a)
			assert.False(t, strings.HasPrefix(a, "--legacy-fold"), "%s's manager passes %s: the cold-turkey revision dropped it", name, a)
		}
	}
}
