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
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/graft"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/grafttask"
	"github.com/mediactl/clustarr/pkg/binpath"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Every argv the manager stamps onto a pool pod (pool.Template) or a graft
// Job's pod (the graft planner's graft.Job) parses through this binary's
// own flags: envtest runs no pods, so nothing else would catch a flag the
// binary no longer takes (spec §10.3.1).
func TestPoolAndGraftArgsParse(t *testing.T) {
	cfg := pool.Config{DataDir: "/data", ExtraArgs: []string{"--log-level=DEBUG", "--log-format=json",
		"--tracing-enabled", "--tracing-endpoint=otel:4317", "--tracing-sample-ratio=0.25"}}
	tp := &transcodev1alpha1.TranscodeProfile{}
	for _, class := range []transcodev1alpha1.Hardware{
		transcodev1alpha1.HardwareCPU, transcodev1alpha1.HardwareNVIDIA, transcodev1alpha1.HardwareIntel,
	} {
		c := pool.Template(tp, class, cfg).Spec.Containers[0]
		require.Equal(t, []string{binpath.Transcode}, c.Command, class)
		f := newFlags()
		require.NoError(t, f.fs.Parse(c.Args), class)
		assert.Equal(t, "/data", *f.dataDir, class)
		assert.Empty(t, *f.graftTask, "a pool pod serves its pool: %s", class)
		assert.Equal(t, slog.LevelDebug, f.lo.Level, class)
		assert.Equal(t, "json", f.lo.Format, class)
		assert.True(t, f.to.Enabled, class)
		assert.Equal(t, "otel:4317", f.to.Endpoint, class)
		assert.InDelta(t, 0.25, f.to.SampleRatio, 1e-9, class)
	}

	// The graft planner's Job (app/remediation/graft.Job): the cpu pool's
	// args with --graft-task spliced in, carrying the task the pod runs.
	mf := &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "pb", UID: "mf-uid"}}
	want := grafttask.Task{Schema: grafttask.TaskSchema, File: schema.Ref{Namespace: "media", Name: "pb", UID: "mf-uid"}, Seq: 7,
		Target: "/data/media/movies/pb.mkv", TargetProbeHash: "h1", Root: "/data/media/movies",
		Donor: "/data/media/movies/.clustarr/donors/u/pb.mka", Language: "en", Anchor: "ja"}
	job, err := graft.Job(cfg, mf, want, "pb-graft-7")
	require.NoError(t, err)
	c := job.Spec.Template.Spec.Containers[0]
	require.Equal(t, []string{binpath.Transcode}, c.Command)
	f := newFlags()
	require.NoError(t, f.fs.Parse(c.Args))
	assert.Equal(t, "/data", *f.dataDir)
	assert.Equal(t, "otel:4317", f.to.Endpoint)
	var got grafttask.Task
	require.NoError(t, json.Unmarshal([]byte(*f.graftTask), &got), "--graft-task is the task's JSON")
	assert.Equal(t, want, got)
}
