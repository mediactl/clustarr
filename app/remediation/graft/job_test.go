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

package graft_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/graft"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/graftplan"
	"github.com/mediactl/clustarr/app/transcode/grafttask"
	"github.com/mediactl/clustarr/pkg/binpath"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/placement"
)

// TestTheGraftJobRunsTheTranscodeBinary is the graft Job's shape (loop spec
// §4.12 "Vehicle"): the cpu pool's pod running `transcode --graft-task` on
// the native image, with NATS (D9) and the pod's name for the record's
// writer, owned by its MediaFile, labelled with its UID and the pool Job
// cache's managed-by label, and still credential-less.
func TestTheGraftJobRunsTheTranscodeBinary(t *testing.T) {
	cfg := pool.Config{Image: "native:test", DataDir: "/data", DataClaimName: "clustarr-data", NATSURL: "nats://nats:4222",
		ExtraArgs: []string{"--log-level=DEBUG"}}
	mf := &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "perfect-blue-1997", UID: "mf-uid"}}
	tk := grafttask.Task{Schema: grafttask.TaskSchema, File: schema.Ref{Namespace: "media", Name: mf.Name, UID: "mf-uid"}, Seq: 100,
		Target: "/data/media/movies/pb.mkv", Language: "en"}
	name := graftplan.JobName(mf.Name, 100)
	job, err := graft.Job(cfg, mf, tk, name)
	require.NoError(t, err)
	assert.Equal(t, name, job.Name)
	assert.Equal(t, "media", job.Namespace)
	assert.Equal(t, "mf-uid", job.Labels[grafttask.LabelGraftOf])
	assert.Equal(t, pool.ManagedByValue, job.Labels[pool.LabelManagedBy], "the pool Job cache holds it, so S15 sees it")
	assert.Equal(t, graft.ComponentGraft, job.Labels["app.kubernetes.io/component"])
	assert.Equal(t, "mf-uid", job.Spec.Template.Labels[grafttask.LabelGraftOf], "its pods carry the file's UID too")
	assert.Equal(t, graft.ComponentGraft, job.Spec.Template.Labels["app.kubernetes.io/component"])
	owner := metav1.GetControllerOf(job)
	require.NotNil(t, owner)
	assert.Equal(t, "MediaFile", owner.Kind)
	assert.Equal(t, mf.Name, owner.Name)
	assert.Equal(t, mf.UID, owner.UID)
	assert.Nil(t, owner.BlockOwnerDeletion, "nothing waits on the Job's deletion: no grant on mediafiles/finalizers")
	assert.Equal(t, int32(0), *job.Spec.BackoffLimit)
	assert.Equal(t, int64(graftplan.JobDeadline.Seconds()), *job.Spec.ActiveDeadlineSeconds)
	require.NotNil(t, job.Spec.TTLSecondsAfterFinished)
	pod := job.Spec.Template.Spec
	require.NotNil(t, pod.AutomountServiceAccountToken)
	assert.False(t, *pod.AutomountServiceAccountToken)
	c := pod.Containers[0]
	assert.Equal(t, cfg.Image, c.Image)
	assert.Equal(t, []string{binpath.Transcode}, c.Command, "the graft Job runs `transcode --graft-task` on the native image")
	require.Len(t, c.Args, 5)
	assert.Equal(t, []string{"--data-dir", "/data", "--graft-task"}, c.Args[:3])
	var decoded grafttask.Task
	require.NoError(t, json.Unmarshal([]byte(c.Args[3]), &decoded))
	assert.Equal(t, tk, decoded)
	assert.Equal(t, "--log-level=DEBUG", c.Args[4])
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	assert.Equal(t, cfg.NATSURL, env["NATS_URL"], "a graft pod answers in clustarr-grafts (D9)")
	assert.Contains(t, env, "POD_NAME", "the record's writer")
	assert.Equal(t, "2576980378", env["GOMEMLIMIT"], "60% of the cpu pool's default 4Gi (smoke finding O9's sweep)")
	assert.NotContains(t, env, pool.EnvProfileUID, "a graft pod belongs to no pool")
	assert.NotContains(t, env, pool.EnvClass)

	other, err := graft.Job(pool.Config{Image: "native:test"}, mf, tk, name)
	require.NoError(t, err)
	assert.Equal(t, []string{"--data-dir", pool.DefaultDataDir, "--graft-task"}, other.Spec.Template.Spec.Containers[0].Args[:3],
		"an unset data dir is the pool's default")
}

// TestGraftJobCarriesTheWorkerPlacement: a graft pod is a cpu pool pod's
// twin, so it takes the workers profile too (placement design §5.6).
func TestGraftJobCarriesTheWorkerPlacement(t *testing.T) {
	workers := placement.Profile{NodeAffinity: []corev1.NodeSelectorRequirement{
		{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.NodeSelectorOpDoesNotExist},
	}}
	cfg := pool.Config{Image: "native:test", WorkerPlacement: workers}
	mf := &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "akira-1988", UID: "mf-uid"}}
	tk := grafttask.Task{Schema: grafttask.TaskSchema, File: schema.Ref{Namespace: "media", Name: mf.Name, UID: "mf-uid"}, Seq: 7}

	job, err := graft.Job(cfg, mf, tk, graftplan.JobName(mf.Name, 7))

	require.NoError(t, err)
	aff := job.Spec.Template.Spec.Affinity
	require.NotNil(t, aff)
	terms := aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	require.Len(t, terms, 1)
	assert.Equal(t, workers.NodeAffinity, terms[0].MatchExpressions)
}
