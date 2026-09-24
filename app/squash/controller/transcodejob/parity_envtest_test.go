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

package transcodejob_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/controller/pool"
	"github.com/mediactl/clustarr/app/squash/worker"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// TestStatusPlanIsTheArgvTheWorkerRenders closes the gap-fix item "the HDR
// arguments [the controller] renders into status.plan may differ from the
// worker's": for a real HDR10 source with mastering metadata, the
// controller plans from the probe summary catalogarr stores, the worker's
// own path (the task worker.BuildTask renders, FromProbe of a live probe,
// ProbeCapabilities, ThreadsFromEnv from the pool template's value) plans
// from the file, and the two argv are the same -- status.plan.argsHash IS the hash of what the worker
// runs, HDR arguments and all, for an in-place job, a container change and
// a kept source (whose .part is beside its own " - <profile>" name) alike.
func TestStatusPlanIsTheArgvTheWorkerRenders(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ffmpeg"); err != nil {
		t.Skip("ffmpeg not present on this box")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH")
	}
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "tj-parity"
	newNamespace(t, c, ns)

	dir := t.TempDir()
	for name, tc := range map[string]struct {
		src  string
		keep bool // policy.replaceSource=false
		// memoryOnly sets resources with no CPU limit: the Downward API
		// would report the node's CPUs, so the Job carries the stated
		// default the planner used instead.
		memoryOnly bool
	}{
		"in-place":         {src: filepath.Join(dir, "Film (2020).mkv")},
		"container-change": {src: filepath.Join(dir, "Other (2020).mp4")},
		"kept-source":      {src: filepath.Join(dir, "Kept (2020).mkv"), keep: true},
		"no-cpu-limit":     {src: filepath.Join(dir, "Unlimited (2020).mkv"), memoryOnly: true},
	} {
		src := tc.src
		t.Run(name, func(t *testing.T) {
			// A 10-bit H.264 HDR10 source -- BT.2020/PQ plus mastering-display
			// and content-light SEI -- so the video is encoded, not remuxed,
			// and every HDR argument is rendered.
			gen := exec.Command("/usr/bin/ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=24:duration=2",
				"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=2",
				"-vf", "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc:range=tv",
				"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le",
				"-x264-params", "mastering-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):cll=1000,400",
				"-c:a", "ac3", "-metadata:s:a:0", "language=eng", src)
			out, err := gen.CombinedOutput()
			require.NoError(t, err, string(out))
			mi, raw, err := mediainfo.Probe(ctx, src)
			require.NoError(t, err)
			require.Equal(t, commonv1.HdrFormatHDR10, mi.Hdr)
			require.NotNil(t, raw.MasteringDisplay, "the source must carry what the summary cannot")

			mfName, profile := "parity-"+strings.ReplaceAll(name, "-", ""), "parity-"+name
			// The clip is 2s; the 1m policy.minDuration default would skip it.
			tp := newProfile(t, c, profile, "hash-"+name, func(p *transcodev1alpha1.TranscodeProfile) {
				p.Spec.Policy.MinDuration = &metav1.Duration{}
				if tc.keep {
					p.Spec.Policy.ReplaceSource = ptr.To(false)
				}
				if tc.memoryOnly {
					p.Spec.Resources = corev1.ResourceRequirements{Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					}}
				}
			})
			mf := newMediaFile(t, c, ns, mfName, "probe-"+name, mi) // catalogarr's stored summary
			newTJ(t, c, ns, mfName, mf.Name, profile, "probe-"+name, func(tj *transcodev1alpha1.TranscodeJob) {
				tj.Spec.SourcePath = src
			})
			reconcileTJ(t, newReconciler(t, c, map[string]int32{"cpu": 0}), ns, mfName)
			tj := getTJ(t, c, ns, mfName)
			require.NotNil(t, tj.Status.Plan, "message: %s", tj.Status.Message)
			assert.Equal(t, "hdr10", tj.Status.Plan.HDRMode)
			require.Equal(t, "libx265", tj.Status.Plan.Encoder)
			class := transcodev1alpha1.HardwareCPU // the class a libx265 plan dispatches to

			// The worker's own inputs, as squasharr dispatches them: the task
			// worker.BuildTask renders, and CLUSTARR_CPU_LIMIT as the class's
			// pool template delivers it.
			folders := []catalogv1alpha1.RootFolder{{Spec: catalogv1alpha1.RootFolderSpec{Path: dir}}}
			tk, err := worker.BuildTask(tj, tp, mf, folders, 1, class)
			require.NoError(t, err)
			cfg := pool.Config{Image: "transcoder:test"}
			t.Setenv(worker.CPULimitEnv, cpuLimitEnv(t, pool.Template(tp, class, cfg)))

			// The worker's own path, as squasharr/worker.Process takes it.
			info, err := transcode.FromProbe(mi, raw)
			require.NoError(t, err)
			info.Path = src
			caps, err := transcode.ProbeCapabilities(ctx, "/usr/bin/ffmpeg")
			require.NoError(t, err)
			plan, err := transcode.Plan(info, worker.ProfileSpec(tk.Profile.Spec, tk.Profile.Hardware), caps, transcode.PlanMeta{
				ProfileName: tk.Profile.Name, ProfileHash: tk.Profile.Hash, Threads: worker.ThreadsFromEnv(), OutputPath: tk.OutputPath,
			})
			require.NoError(t, err)
			require.Equal(t, transcode.DecisionEncode, plan.Decision)

			assert.Equal(t, tj.Status.Plan.VideoArgs, plan.VideoArgs, "the recorded video arguments are the worker's")
			assert.Equal(t, tj.Status.Plan.ArgsHash, transcode.ArgsHash(plan), "status.plan.argsHash is the worker's argv")
			assert.Contains(t, strings.Join(plan.VideoArgs, " "), "hdr10=1")
		})
	}
}

// cpuLimitEnv is worker.CPULimitEnv as the kubelet hands it to a pool pod's
// container: a literal value as written, or the Downward API's limits.cpu
// with divisor 1, which rounds up to whole cores. With no CPU limit the
// Downward API would report the node's allocatable CPU, which no plan can
// know -- so a pool without one must carry a literal.
func cpuLimitEnv(t *testing.T, tmpl corev1.PodTemplateSpec) string {
	t.Helper()
	ctr := tmpl.Spec.Containers[0]
	for _, e := range ctr.Env {
		if e.Name != worker.CPULimitEnv {
			continue
		}
		if e.ValueFrom == nil {
			return e.Value
		}
		require.NotNil(t, e.ValueFrom.ResourceFieldRef)
		require.Equal(t, "limits.cpu", e.ValueFrom.ResourceFieldRef.Resource)
		cpu, ok := ctr.Resources.Limits[corev1.ResourceCPU]
		require.True(t, ok, "the Downward API is wired only when the container has a CPU limit")
		return strconv.FormatInt((cpu.MilliValue()+999)/1000, 10)
	}
	t.Fatalf("the pool template has no %s", worker.CPULimitEnv)
	return ""
}
