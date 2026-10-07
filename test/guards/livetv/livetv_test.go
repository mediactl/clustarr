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

// Package livetv_test holds both installers' Live TV objects to the manager
// they serve (Live TV spec 2026-10-07 §5.0, §5.3): the one livetv Service,
// the tuner's port and flags, and the NetworkPolicy that guards the tuner
// port alone.
package livetv_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"net"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/mediactl/clustarr/internal/cli/manager"
)

// rendered is one installer's objects this test reads.
type rendered struct {
	name        string
	deployments []appsv1.Deployment
	services    []corev1.Service
	policies    []networkingv1.NetworkPolicy
}

func (r rendered) manager(t *testing.T) (*appsv1.Deployment, *corev1.Container) {
	t.Helper()
	for i := range r.deployments {
		if r.deployments[i].Spec.Template.Labels["app.kubernetes.io/component"] == "manager" {
			return &r.deployments[i], &r.deployments[i].Spec.Template.Spec.Containers[0]
		}
	}
	t.Fatalf("%s renders no manager Deployment", r.name)
	return nil, nil
}

func (r rendered) livetvService(t *testing.T) *corev1.Service {
	t.Helper()
	for i := range r.services {
		if strings.HasSuffix(r.services[i].Name, "livetv") {
			return &r.services[i]
		}
	}
	t.Fatalf("%s renders no livetv Service", r.name)
	return nil
}

// installers renders the chart with args, and kustomize's deployable form.
func installers(t *testing.T, helmArgs ...string) []rendered {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	helm, kustomize := tool(t, "helm"), tool(t, "kustomize")
	return []rendered{
		decode(t, "helm", run(t, root, helm, append([]string{"template", "clustarr", "charts/clustarr", "--namespace", "clustarr-system"}, helmArgs...)...)),
		decode(t, "kustomize", run(t, root, kustomize, "build", "config/default")),
	}
}

func tool(t *testing.T, name string) string {
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

func run(t *testing.T, dir, bin string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), "%s %s\n%s", bin, strings.Join(args, " "), stderr.String())
	return stdout.Bytes()
}

func decode(t *testing.T, name string, in []byte) rendered {
	t.Helper()
	r := rendered{name: name}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(in), 4096)
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); errors.Is(err, io.EOF) {
			return r
		} else if err != nil {
			t.Fatalf("decode %s's render: %v", name, err)
		}
		into := func(v any) { require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(obj, v)) }
		switch obj["kind"] {
		case "Deployment":
			var d appsv1.Deployment
			into(&d)
			r.deployments = append(r.deployments, d)
		case "Service":
			var s corev1.Service
			into(&s)
			r.services = append(r.services, s)
		case "NetworkPolicy":
			var p networkingv1.NetworkPolicy
			into(&p)
			r.policies = append(r.policies, p)
		}
	}
}

// arg is the value of --name in args.
func arg(args []string, name string) (string, bool) {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
			return v, true
		}
	}
	return "", false
}

func subset(sub, of map[string]string) bool {
	for k, v := range sub {
		if of[k] != v {
			return false
		}
	}
	return len(sub) > 0
}

func portNamed(c *corev1.Container, name string) (corev1.ContainerPort, bool) {
	for _, p := range c.Ports {
		if p.Name == name {
			return p, true
		}
	}
	return corev1.ContainerPort{}, false
}

func TestLiveTVServiceSelectsTheManager(t *testing.T) {
	for _, r := range installers(t) {
		t.Run(r.name, func(t *testing.T) {
			dep, ctr := r.manager(t)
			svc := r.livetvService(t)
			assert.True(t, subset(svc.Spec.Selector, dep.Spec.Template.Labels), "the livetv Service selects the manager: %v", svc.Spec.Selector)
			for _, other := range r.deployments {
				if other.Name != dep.Name {
					assert.False(t, subset(svc.Spec.Selector, other.Spec.Template.Labels), "and not %s", other.Name)
				}
			}
			require.Len(t, svc.Spec.Ports, 1)
			assert.Equal(t, int32(80), svc.Spec.Ports[0].Port, "status.address is the Service on port 80")
			assert.Equal(t, "livetv", svc.Spec.Ports[0].TargetPort.String())

			port, ok := portNamed(ctr, "livetv")
			require.True(t, ok, "the manager container declares a livetv port")
			bind, ok := arg(ctr.Args, "livetv-bind-address")
			require.True(t, ok, "--livetv-bind-address")
			_, p, err := net.SplitHostPort(bind)
			require.NoError(t, err)
			assert.Equal(t, p, strconv.Itoa(int(port.ContainerPort)), "the tuner listens on the port the Service targets")
			name, _ := arg(ctr.Args, "livetv-service")
			assert.Equal(t, svc.Name, name, "--livetv-service names the Service")
			portRange, _ := arg(ctr.Args, "livetv-port-range")
			assert.Equal(t, "47000-47999", portRange)
		})
	}
}

func TestLiveTVPolicyListsEveryManagerPort(t *testing.T) {
	for _, r := range installers(t, "--set", "autoscaling.enabled=true") {
		t.Run(r.name, func(t *testing.T) {
			dep, ctr := r.manager(t)
			var pol *networkingv1.NetworkPolicy
			for i := range r.policies {
				if subset(r.policies[i].Spec.PodSelector.MatchLabels, dep.Spec.Template.Labels) {
					require.Nil(t, pol, "one policy selects the manager")
					pol = &r.policies[i]
				}
			}
			require.NotNil(t, pol, "a NetworkPolicy selects the manager pod")
			assert.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, pol.Spec.PolicyTypes)

			others := map[string]bool{}
			for _, p := range ctr.Ports {
				if p.Name != "livetv" {
					others[p.Name] = true
				}
			}
			require.NotEmpty(t, others)
			var guarded, open int
			for _, rule := range pol.Spec.Ingress {
				ports := map[string]bool{}
				for _, p := range rule.Ports {
					require.NotNil(t, p.Port)
					ports[p.Port.String()] = true
				}
				if len(rule.From) > 0 {
					guarded++
					assert.Equal(t, map[string]bool{"livetv": true}, ports, "only the tuner port is limited to its peers")
					// Exactly the default peer: kustomize's includeSelectors
					// labels would add clustarr's own labels, which no Plex
					// pod carries.
					require.Len(t, rule.From, 1)
					require.NotNil(t, rule.From[0].PodSelector)
					assert.Equal(t, map[string]string{"app.kubernetes.io/component": "plex"}, rule.From[0].PodSelector.MatchLabels, "Plex's pods, by cluster-plex's label alone")
				} else {
					open++
					assert.Equal(t, others, ports, "every other manager port stays open: %v", slices.Sorted(maps.Keys(others)))
				}
			}
			assert.Equal(t, 1, guarded, "one rule for the tuner port")
			assert.Equal(t, 1, open, "one rule for the manager's other ports")
		})
	}
}

func TestLiveTVDisabledRendersNoPolicyAndPortZero(t *testing.T) {
	r := installers(t, "--set", "livetv.enabled=false")[0]
	_, ctr := r.manager(t)
	assert.Empty(t, r.policies, "no NetworkPolicy")
	bind, _ := arg(ctr.Args, "livetv-bind-address")
	assert.Equal(t, "0", bind)
	_, ok := portNamed(ctr, "livetv")
	assert.False(t, ok, "no tuner port")
	for _, s := range r.services {
		assert.False(t, strings.HasSuffix(s.Name, "livetv"), "no livetv Service: %s", s.Name)
	}
}

func TestBothInstallersLiveTVArgsParseAsTheManager(t *testing.T) {
	for _, r := range installers(t) {
		t.Run(r.name, func(t *testing.T) {
			_, ctr := r.manager(t)
			var args []string
			for _, a := range ctr.Args {
				if strings.HasPrefix(a, "--livetv-") {
					args = append(args, a)
				}
			}
			require.Len(t, args, 3)
			var got manager.Options
			cmd := manager.NewCommandWith(func(_ context.Context, o manager.Options) error { got = o; return nil })
			cmd.SetArgs(append(args, "--nats-url=nats://nats:4222", "--native-image=clustarr/native:dev", "--leader-election-namespace=clustarr-system"))
			require.NoError(t, cmd.Execute(), "the manager's own flag checks pass")
			bind, _ := arg(args, "livetv-bind-address")
			portRange, _ := arg(args, "livetv-port-range")
			svc, _ := arg(args, "livetv-service")
			assert.Equal(t, manager.Options{LiveTVBindAddress: bind, LiveTVPortRange: portRange, LiveTVService: svc},
				manager.Options{LiveTVBindAddress: got.LiveTVBindAddress, LiveTVPortRange: got.LiveTVPortRange, LiveTVService: got.LiveTVService})
		})
	}
}
