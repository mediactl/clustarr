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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/mediactl/clustarr/internal/cli/agent"
	"github.com/mediactl/clustarr/pkg/binpath"
	"github.com/mediactl/clustarr/test/installtest"
)

// renders caches installerRenders: each render takes seconds, and three
// tests read the same three.
var renders struct {
	once sync.Once
	r    map[string]installtest.Rendered
}

// installerRenders is what each installer renders: the chart's defaults,
// the chart with the Postgres release index, and kustomize's config/default.
func installerRenders(t *testing.T) map[string]installtest.Rendered {
	t.Helper()
	renders.once.Do(func() {
		renders.r = map[string]installtest.Rendered{
			"helm":           installtest.DecodeRendered(t, installtest.RenderChart(t)),
			"helm, postgres": installtest.DecodeRendered(t, installtest.RenderChart(t, "--set", "postgres.enabled=true")),
			"kustomize":      installtest.DecodeRendered(t, installtest.BuildKustomize(t, "config/default")),
		}
	})
	// A render that failed (or skipped) in the first caller left nothing;
	// ranging over nil would pass every later caller silently.
	require.Len(t, renders.r, 3, "the installer renders failed in an earlier test")
	return renders.r
}

// renderedContainer is the container named name, with its Deployment, in
// one installer's render; the chart prefixes Deployment names with the
// release, so a container is found by its own name, which both installers
// give as the component.
func renderedContainer(t *testing.T, r installtest.Rendered, name string) (appsv1.Deployment, corev1.Container) {
	t.Helper()
	var found []appsv1.Deployment
	var c corev1.Container
	for _, d := range r.Deployments {
		for _, cc := range d.Spec.Template.Spec.Containers {
			if cc.Name == name {
				found = append(found, d)
				c = cc
			}
		}
	}
	require.Len(t, found, 1, "exactly one Deployment runs a container %q", name)
	return found[0], c
}

// setContainerEnv puts c's environment into the test's, as the kubelet
// would for pod: literal values as written, the downward API's fields from
// pod, and a Secret's key as a placeholder naming it (the value is never
// dialled: the recording run opens nothing).
func setContainerEnv(t *testing.T, c corev1.Container, pod *corev1.Pod) {
	t.Helper()
	for _, e := range c.Env {
		switch {
		case e.ValueFrom == nil:
			t.Setenv(e.Name, e.Value)
		case e.ValueFrom.FieldRef != nil:
			v, ok := map[string]string{
				"metadata.name":      pod.Name,
				"metadata.namespace": pod.Namespace,
				"spec.nodeName":      pod.Spec.NodeName,
			}[e.ValueFrom.FieldRef.FieldPath]
			require.True(t, ok, "%s: env %s reads field %s, which this test does not model", c.Name, e.Name, e.ValueFrom.FieldRef.FieldPath)
			t.Setenv(e.Name, v)
		case e.ValueFrom.SecretKeyRef != nil:
			t.Setenv(e.Name, "secret:"+e.ValueFrom.SecretKeyRef.Name+"/"+e.ValueFrom.SecretKeyRef.Key)
		default:
			t.Fatalf("%s: env %s has a source this test does not model", c.Name, e.Name)
		}
	}
}

// agentDomainsInstalled is every domain an installer runs as a Deployment;
// the engines' pods are the manager's DownloadClient controller's
// (TestEngineArgvParses).
var agentDomainsInstalled = []string{"catalog", "events", "metadata", "import", "index", "caption"}

// TestInstallerArgsParseAsTheirDomain: each agent container both installers
// render names this binary and parses through its command tree, with its
// environment, into valid options for the domain its Deployment is named
// for. envtest runs no pods, so this is the only check that the argv and
// environment an installer ships start the agent at all.
func TestInstallerArgsParseAsTheirDomain(t *testing.T) {
	for name, r := range installerRenders(t) {
		for _, domain := range agentDomainsInstalled {
			t.Run(name+" "+domain, func(t *testing.T) {
				d, c := renderedContainer(t, r, "agent-"+domain)
				require.Equal(t, []string{binpath.Agent}, c.Command)
				pod := &corev1.Pod{}
				pod.Name, pod.Namespace, pod.Spec.NodeName = d.Name+"-7c9f8d6b5-x2x4q", "clustarr-system", "node-a"
				setContainerEnv(t, c, pod)

				var got agent.Options
				ran := false
				cmd := agent.NewCommandWith(func(_ context.Context, o agent.Options) error { got, ran = o, true; return nil })
				cmd.SetArgs(c.Args)
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				require.NoError(t, cmd.Execute(), "agent %v", c.Args)
				require.True(t, ran, "agent %v did not reach its run", c.Args)
				require.NoError(t, got.Validate())
				assert.Equal(t, domain, got.Domain)
				assert.Equal(t, "clustarr-system", got.Namespace, "the namespace comes from the downward API")
				assert.NotEmpty(t, got.NATSURL)
				switch domain {
				case "index":
					assert.Equal(t, agent.DefaultIndexPath, got.IndexPath)
					assert.NotEmpty(t, got.FacadeAPIKeySecret)
					if strings.Contains(name, "postgres") {
						assert.NotEmpty(t, got.IndexDSN, "the Postgres index reaches --index-dsn from the CNPG Secret")
					} else {
						assert.Empty(t, got.IndexDSN, "SQLite unless Postgres is enabled")
					}
				case "import", "caption":
					assert.Equal(t, "/data", got.DataDir)
				}
			})
		}
	}
}
