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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/mediactl/clustarr/test/installtest"
)

// The logging ConfigMap of the 2026-11-10 structured-logging design (§6):
// one per install, mounted read-only at /etc/clustarr in every pod the
// installers render, as a directory and never a subPath, since a subPath
// mount never receives the ConfigMap's updates and the live level knob
// would silently do nothing (Review Focus 1).

const loggingMountPath = "/etc/clustarr"

// ours reports whether a pod template is one of clustarr's own, not a
// subchart's (nats-box, cluster-plex).
func ours(labels map[string]string) bool { return labels["app.kubernetes.io/name"] == "clustarr" }

func loggingMounts(t *testing.T, name string, out []byte) {
	t.Helper()
	r := installtest.DecodeRendered(t, out)
	require.NotEmpty(t, r.Deployments, "%s renders no Deployment", name)
	for _, d := range r.Deployments {
		if !ours(d.Spec.Template.Labels) {
			continue // the nats and cluster-plex subcharts' pods
		}
		var volume *corev1.Volume
		for i := range d.Spec.Template.Spec.Volumes {
			v := &d.Spec.Template.Spec.Volumes[i]
			if v.ConfigMap != nil && strings.HasSuffix(v.ConfigMap.Name, "-logging") {
				volume = v
			}
		}
		if !assert.NotNil(t, volume, "%s: %s mounts no logging ConfigMap", name, d.Name) {
			continue
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			var mount *corev1.VolumeMount
			for i := range c.VolumeMounts {
				if c.VolumeMounts[i].Name == volume.Name {
					mount = &c.VolumeMounts[i]
				}
			}
			if !assert.NotNil(t, mount, "%s: %s/%s does not mount the logging ConfigMap", name, d.Name, c.Name) {
				continue
			}
			assert.Equal(t, loggingMountPath, mount.MountPath, "%s: %s/%s", name, d.Name, c.Name)
			assert.True(t, mount.ReadOnly, "%s: %s/%s mounts the logging ConfigMap writable", name, d.Name, c.Name)
			assert.Empty(t, mount.SubPath, "%s: %s/%s mounts the logging ConfigMap with a subPath, which never receives its updates", name, d.Name, c.Name)
		}
	}
}

func TestEveryPodMountsTheLoggingConfig(t *testing.T) {
	for name, out := range map[string][]byte{
		"helm template clustarr":   installtest.RenderChart(t),
		"kustomize config/default": installtest.BuildKustomize(t, "config/default"),
	} {
		loggingMounts(t, name, out)
	}
}

// loggingYAML is the logging.yaml the chart renders into its ConfigMap under
// the given --set values, parsed.
func loggingYAML(t *testing.T, set ...string) map[string]any {
	t.Helper()
	var args []string
	for _, s := range set {
		args = append(args, "--set", s)
	}
	for _, obj := range installtest.Objects(t, installtest.RenderChart(t, args...)) {
		if obj.GetKind() != "ConfigMap" || !strings.HasSuffix(obj.GetName(), "-logging") {
			continue
		}
		data, _, _ := unstructuredNestedString(obj.Object, "data", "logging.yaml")
		var doc struct {
			Logging map[string]any `json:"logging"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(data), &doc), data)
		return doc.Logging
	}
	t.Fatal("the chart renders no -logging ConfigMap")
	return nil
}

func unstructuredNestedString(obj map[string]any, fields ...string) (string, bool, error) {
	cur := any(obj)
	for _, f := range fields {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false, nil
		}
		cur, ok = m[f]
		if !ok {
			return "", false, nil
		}
	}
	s, ok := cur.(string)
	return s, ok, nil
}

// The knob is global.logging, which the cluster-plex subchart reads too;
// clustarr's own logging block overrides it key by key for clustarr's
// pods, its subsystems merged over the global's (design §6).
func TestTheLoggingValuesRender(t *testing.T) {
	def := loggingYAML(t)
	assert.Equal(t, map[string]any{"level": "info", "format": "json", "addSource": false}, def)

	global := loggingYAML(t, "global.logging.level=debug", "global.logging.subsystems.livetv=warn")
	assert.Equal(t, "debug", global["level"])
	assert.Equal(t, map[string]any{"livetv": "warn"}, global["subsystems"])

	both := loggingYAML(t, "global.logging.level=debug", "global.logging.subsystems.livetv=warn",
		"logging.level=warn", "logging.subsystems.livetv\\.tuner=trace")
	assert.Equal(t, "warn", both["level"], "clustarr's own level beats the global's")
	assert.Equal(t, map[string]any{"livetv": "warn", "livetv.tuner": "trace"}, both["subsystems"], "the subsystems merge")
}

// app.kubernetes.io/version is the build the pods run, the image tag, not
// the chart's appVersion (design §2).
func TestTheChartVersionLabelIsTheImage(t *testing.T) {
	r := installtest.DecodeRendered(t, installtest.RenderChart(t, "--set", "image.clustarr.tag=sha-abc123"))
	require.NotEmpty(t, r.Deployments)
	for _, d := range r.Deployments {
		if !ours(d.Spec.Template.Labels) || d.Spec.Template.Labels["app.kubernetes.io/component"] == "markers" {
			continue // a subchart's pod; markers runs the native image, below
		}
		assert.Equal(t, "sha-abc123", d.Spec.Template.Labels["app.kubernetes.io/version"], d.Name)
	}
	r = installtest.DecodeRendered(t, installtest.RenderChart(t, "--set", "image.native.tag=sha-native9"))
	for _, d := range r.Deployments {
		if d.Spec.Template.Labels["app.kubernetes.io/component"] == "markers" {
			assert.Equal(t, "sha-native9", d.Spec.Template.Labels["app.kubernetes.io/version"], d.Name)
		}
	}
	// With no tag set the image's tag is the appVersion, and so is the label.
	r = installtest.DecodeRendered(t, installtest.RenderChart(t))
	for _, d := range r.Deployments {
		if !ours(d.Spec.Template.Labels) {
			continue
		}
		image := d.Spec.Template.Spec.Containers[0].Image
		tag := image[strings.LastIndex(image, ":")+1:]
		assert.Equal(t, tag, d.Spec.Template.Labels["app.kubernetes.io/version"], "%s: the label is not the image's tag (%s)", d.Name, image)
	}
}
