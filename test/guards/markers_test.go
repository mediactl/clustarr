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
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/mediactl/clustarr/pkg/binpath"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/test/installtest"
)

// markersPodMemory and markersSlotMemory size markers' memory limit
// (values.yaml markers.resources, config/manager/markers.yaml): a pod's own
// share -- the Go runtime and FFmpeg's libraries (53 MB measured) and ONNX
// Runtime's session and arena (estimated) -- and each slot's. The slot's is
// measured (2026-10-15, pkg/segments/decode on FFmpeg 9 at two decode
// threads, 16 to 48 rounds of Audio, Frames and Frame a slot) under
// markersMmapThreshold: 4K HEVC Main 10 peaked at 350-390 MB a slot (one,
// two and eight slots), 1080p HEVC Main 10 at 95 MB and 1080p H.264 at
// 40 MB. FFmpeg's frame pools live in the C heap, outside GOMEMLIMIT.
const (
	markersPodMemory  = 256 << 20
	markersSlotMemory = 448 << 20
)

// markersMmapThreshold fixes glibc's mmap threshold at 128 KiB, so FFmpeg's
// frame buffers are unmapped when freed: under glibc's sliding threshold
// they stayed in its arenas, and eight 4K slots held 7.3 GB, two 2.2-3.4 GB,
// past any limit sized from the live set.
const markersMmapThreshold = "glibc.malloc.mmap_threshold=131072"

// TestMarkersHasNoCredentials: markers reads /data and talks NATS only
// (segment detection §4.1), so neither its ServiceAccount nor its pod mounts
// a token and nothing binds the account; it runs /usr/bin/markers with /data
// read-only.
func TestMarkersHasNoCredentials(t *testing.T) {
	for name, out := range map[string][]byte{
		"helm":      installtest.RenderChart(t),
		"kustomize": installtest.BuildKustomize(t, "config/default"),
	} {
		t.Run(name, func(t *testing.T) {
			objs := installtest.Objects(t, out)
			var dep, sa *unstructured.Unstructured
			for _, o := range objs {
				if o.GetKind() == "Deployment" && strings.HasSuffix(o.GetName(), "markers") {
					dep = o
				}
			}
			require.NotNil(t, dep, "no markers Deployment")
			spec, _, _ := unstructured.NestedMap(dep.Object, "spec", "template", "spec")
			assert.Equal(t, false, spec["automountServiceAccountToken"], "the pod mounts no token")
			account, _ := spec["serviceAccountName"].(string)
			require.NotEmpty(t, account)
			var bindings int
			for _, o := range objs {
				switch o.GetKind() {
				case "ServiceAccount":
					if o.GetName() == account {
						sa = o
					}
				case "RoleBinding", "ClusterRoleBinding":
					bindings++
					subjects, _, _ := unstructured.NestedSlice(o.Object, "subjects")
					for _, s := range subjects {
						assert.NotEqual(t, account, s.(map[string]any)["name"], "%s %s binds markers' account", o.GetKind(), o.GetName())
					}
				}
			}
			require.Positive(t, bindings, "%s renders no binding; this guard is reading the wrong render", name)
			require.NotNil(t, sa, "the markers ServiceAccount is rendered")
			automount, found, _ := unstructured.NestedBool(sa.Object, "automountServiceAccountToken")
			assert.True(t, found && !automount, "the markers ServiceAccount mounts no token")
			containers, _, _ := unstructured.NestedSlice(spec, "containers")
			require.Len(t, containers, 1)
			c := containers[0].(map[string]any)
			assert.Equal(t, []any{binpath.Markers}, c["command"])
			env := map[string]bool{}
			for _, e := range c["env"].([]any) {
				env[e.(map[string]any)["name"].(string)] = true
			}
			assert.True(t, env["NATS_URL"] && env["POD_NAME"], "NATS_URL and POD_NAME")
			var data bool
			for _, m := range c["volumeMounts"].([]any) {
				if mm := m.(map[string]any); mm["mountPath"] == "/data" {
					data = true
					assert.Equal(t, true, mm["readOnly"], "/data is read-only")
				}
			}
			assert.True(t, data, "markers mounts /data")
		})
	}
}

// markersContainer is the markers container in one render.
func markersContainer(t *testing.T, out []byte) corev1.Container {
	t.Helper()
	d, ok := installtest.Workloads(installtest.DecodeRendered(t, out))["markers"]
	require.True(t, ok, "no markers Deployment")
	require.Len(t, d.Spec.Template.Spec.Containers, 1)
	return d.Spec.Template.Spec.Containers[0]
}

// TestMarkersResourcesLetAPodUseItsNode: a markers pod sizes its slots from
// the CPUs it can use (GOMAXPROCS) unless CLUSTARR_CONSUMER_SLOTS overrides
// them, so neither installer caps its CPU -- a limit of 6 held talos'
// 16-CPU worker to three slots while its 4-CPU workers ran three too
// (2026-10-15) -- each keeps a CPU request, sets no override, passes the
// default --cores-per-slot, and sets a memory limit that holds the most
// slots a pod may size itself to, under the mmap threshold its per-slot
// figure was measured with. The chart's markers.coresPerSlot and
// markers.consumerSlots reach the pod.
func TestMarkersResourcesLetAPodUseItsNode(t *testing.T) {
	for name, out := range map[string][]byte{
		"helm":      installtest.RenderChart(t),
		"kustomize": installtest.BuildKustomize(t, "config/default"),
	} {
		t.Run(name, func(t *testing.T) {
			c := markersContainer(t, out)
			_, capped := c.Resources.Limits[corev1.ResourceCPU]
			assert.False(t, capped, "markers' CPU limit would cap GOMAXPROCS, its slots and its decode threads")
			req, ok := c.Resources.Requests[corev1.ResourceCPU]
			assert.True(t, ok && req.Sign() > 0, "markers keeps a CPU request")
			_, ok = c.Resources.Requests[corev1.ResourceMemory]
			assert.True(t, ok, "markers keeps a memory request")
			limit, ok := c.Resources.Limits[corev1.ResourceMemory]
			require.True(t, ok, "markers sets a memory limit")
			need := resource.NewQuantity(markersPodMemory+events.PerNodeSlotsCeiling*markersSlotMemory, resource.BinarySI)
			assert.GreaterOrEqual(t, limit.Cmp(*need), 0, "markers' memory limit %s holds %d slots: %s",
				limit.String(), events.PerNodeSlotsCeiling, need.String())
			tunables, _ := containerEnv(c, "GLIBC_TUNABLES")
			assert.Equal(t, markersMmapThreshold, tunables, "the memory limit holds only under a fixed mmap threshold")
			_, set := containerEnv(c, events.SlotsEnv)
			assert.False(t, set, "markers sizes its slots from its node unless told otherwise")
			assert.Contains(t, c.Args, "--cores-per-slot="+strconv.Itoa(events.DefaultCoresPerSlot))
		})
	}
	c := markersContainer(t, installtest.RenderChart(t, "--set", "markers.coresPerSlot=1",
		"--set", "markers.consumerSlots."+events.ConsumerMarkersAnalyze+"=3"))
	assert.Contains(t, c.Args, "--cores-per-slot=1", "markers.coresPerSlot")
	v, _ := containerEnv(c, events.SlotsEnv)
	assert.Equal(t, events.ConsumerMarkersAnalyze+"=3", v, "markers.consumerSlots still overrides")
}
