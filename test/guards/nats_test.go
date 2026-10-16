/*
Copyright 2026 The clustarr Authors.

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
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/mediactl/clustarr/test/installtest"
)

// natsContainer is the "nats" container of the one NATS StatefulSet an
// installer renders.
func natsContainer(t *testing.T, out []byte) corev1.Container {
	t.Helper()
	var found []corev1.Container
	for _, obj := range installtest.Objects(t, out) {
		if obj.GetKind() != "StatefulSet" {
			continue
		}
		var sts appsv1.StatefulSet
		require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &sts))
		for _, c := range sts.Spec.Template.Spec.Containers {
			if c.Name == "nats" {
				found = append(found, c)
			}
		}
	}
	require.Len(t, found, 1, "NATS server containers")
	return found[0]
}

// TestBothInstallersSizeNATSForAReconnectStorm is smoke finding O10: the
// chart's NATS ran with a 1Gi memory limit and was OOM-killed when every
// client reconnected at once during the first upgrade of a library of
// 15,000 episodes and 12,000 files. Both installers run it with a 2Gi limit,
// a request equal to it (Guaranteed QoS, so a quorum member is never evicted
// under node memory pressure; 2026-10-16) and GOMEMLIMIT 1800MiB -- the
// server's soft limit under the cgroup's, so its collector runs hard before
// the kernel kills it.
func TestBothInstallersSizeNATSForAReconnectStorm(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	helm := installtest.FindTool(t, "helm")
	kustomize := installtest.FindTool(t, "kustomize")
	for name, out := range map[string][]byte{
		"helm template charts/clustarr":  installtest.Run(t, root, helm, "template", "clustarr", "charts/clustarr"),
		"kustomize build config/default": installtest.Run(t, root, kustomize, "build", "config/default"),
	} {
		c := natsContainer(t, out)
		limit, request := c.Resources.Limits[corev1.ResourceMemory], c.Resources.Requests[corev1.ResourceMemory]
		assert.Equal(t, "2Gi", limit.String(), "%s: the NATS memory limit", name)
		assert.Equal(t, "2Gi", request.String(), "%s: the NATS memory request", name)
		var gomemlimit string
		for _, e := range c.Env {
			if e.Name == "GOMEMLIMIT" {
				gomemlimit = e.Value
			}
		}
		assert.Equal(t, "1800MiB", gomemlimit, "%s: the NATS server's GOMEMLIMIT", name)
	}
}

// TestBothInstallersRaiseTheNATSMaxPayload holds the chart's NATS
// configuration to the limit config/nats/configmap.yaml has always set
// (design spec §12): max_payload 8Mi. The nats subchart's default is the
// server's 1 MiB, under which index's rpc.index.download reply -- the
// .nzb inline, base64 in JSON -- is refused for any 1080p movie's ~1.3 MB
// .nzb, and the grab sits in Assigned forever (2026-09-24, the owner's
// cluster, Helm release revision 36). The two installers render the value
// differently (the subchart emits JSON, kustomize a conf block), so this
// matches each in its own spelling rather than comparing documents.
func TestBothInstallersRaiseTheNATSMaxPayload(t *testing.T) {
	helm := installtest.FindTool(t, "helm")
	kustomize := installtest.FindTool(t, "kustomize")

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}

	chart := installtest.Run(t, root, helm, "template", "clustarr", "charts/clustarr")
	if want := regexp.MustCompile(`"max_payload":\s*8Mi\b`); !want.Match(chart) {
		t.Errorf("helm template charts/clustarr renders no NATS max_payload of 8Mi; "+
			"nats.config.merge.max_payload in charts/clustarr/values.yaml must be %q", "<< 8Mi >>")
	}

	kz := installtest.Run(t, root, kustomize, "build", "config/default")
	if want := regexp.MustCompile(`(?m)^\s*max_payload:\s*8Mi\b`); !want.Match(kz) {
		t.Errorf("kustomize build config/default renders no NATS max_payload of 8Mi; " +
			"config/nats/configmap.yaml must keep it")
	}
}
