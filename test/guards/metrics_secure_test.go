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
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/mediactl/clustarr/internal/cli/ctrlflags"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/test/installtest"
)

// metricsAgents are the agent Deployments the chart renders by default, each
// serving controller-runtime's metrics on :8443 as the manager does.
var metricsAgents = []string{"catalog", "events", "metadata", "import", "index", "caption"}

// TestMetricsSecureReachesEveryControllerRuntimeProcess: metrics.secure=false
// serves the manager's and every agent's metrics as plain HTTP, for a scraper
// that cannot speak TLS to a pod -- talos's Alloy scrapes every declared port
// over http (2026-10-15) -- and left alone they stay HTTPS. The rendered args
// are parsed by the binaries' own flag sets, so a flag the chart misspells
// fails here rather than as a crash-looping pod. ui and markers bind no
// --metrics-secure, so they must never be handed it.
func TestMetricsSecureReachesEveryControllerRuntimeProcess(t *testing.T) {
	for name, tc := range map[string]struct {
		args   []string
		secure bool
	}{
		"default": {secure: true},
		"off":     {args: []string{"--set", "metrics.secure=false"}, secure: false},
	} {
		t.Run(name, func(t *testing.T) {
			r := installtest.DecodeRendered(t, installtest.RenderChart(t, tc.args...))
			parse := func(component string, bind func(*pflag.FlagSet) *k8s.Options) {
				t.Helper()
				c := installtest.Container(t, r, component)
				fs := pflag.NewFlagSet(component, pflag.ContinueOnError)
				fs.ParseErrorsAllowlist.UnknownFlags = true
				o := bind(fs)
				require.NoError(t, fs.Parse(c.Args), component)
				require.Equal(t, tc.secure, o.MetricsSecure,
					"%s: the chart's metrics.secure did not reach its --metrics-secure", component)
			}
			parse("manager", ctrlflags.BindManager)
			for _, a := range metricsAgents {
				parse("agent-"+a, ctrlflags.BindAgent)
			}
			for _, component := range []string{"ui", "markers"} {
				for _, a := range installtest.Container(t, r, component).Args {
					require.NotContains(t, a, "--metrics-secure", "%s binds no --metrics-secure; it would refuse to start", component)
				}
			}
		})
	}
}

// TestServiceMonitorsFollowMetricsSecure: a ServiceMonitor scrapes the scheme
// the pods serve. Plain HTTP carries no service-account token -- nothing on
// that endpoint reads one, and it would cross the network in the clear -- and
// no tlsConfig. markers serves plain HTTP either way and keeps its own.
func TestServiceMonitorsFollowMetricsSecure(t *testing.T) {
	for name, tc := range map[string]struct {
		args   []string
		scheme string
	}{
		"default": {scheme: "https"},
		"off":     {args: []string{"--set", "metrics.secure=false"}, scheme: "http"},
	} {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"--set", "metrics.serviceMonitor.enabled=true"}, tc.args...)
			seen := 0
			for _, o := range installtest.Objects(t, installtest.RenderChart(t, args...)) {
				if o.GetKind() != "ServiceMonitor" || o.GetLabels()["app.kubernetes.io/component"] == "markers" {
					continue
				}
				eps, found, err := unstructured.NestedSlice(o.Object, "spec", "endpoints")
				require.NoError(t, err, o.GetName())
				require.True(t, found, o.GetName())
				require.Len(t, eps, 1, o.GetName())
				ep, ok := eps[0].(map[string]any)
				require.True(t, ok, o.GetName())
				require.Equal(t, tc.scheme, ep["scheme"], "%s scrapes the wrong scheme", o.GetName())
				_, tls := ep["tlsConfig"]
				require.Equal(t, tc.scheme == "https", tls, "%s: tlsConfig belongs to an https scrape only", o.GetName())
				_, token := ep["bearerTokenFile"]
				require.Equal(t, tc.scheme == "https", token, "%s: a bearer token belongs to an https scrape only", o.GetName())
				seen++
			}
			require.Equal(t, 1+len(metricsAgents), seen, "one ServiceMonitor for the manager and each agent")
		})
	}
}
