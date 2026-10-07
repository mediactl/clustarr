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

package graft

import (
	"cmp"
	"encoding/json"
	"slices"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/graftplan"
	"github.com/mediactl/clustarr/app/transcode/grafttask"
)

// ComponentGraft is a graft Job's (and its pods') component label.
const ComponentGraft = "transcode-graft"

// jobTTL is how long a finished graft Job stays for an operator to read; the
// planner deletes an answered one at once.
const jobTTL = 24 * time.Hour

// Job renders the standalone graft Job of a dispatch (loop spec §4.12
// "Vehicle"): the cpu pool's pod running `transcode --graft-task <t>`,
// keeping NATS_URL and POD_NAME (it answers in clustarr-grafts, D9) and
// mounting no ServiceAccount token (pool.Template's), controlled by its
// MediaFile, labelled with the file's UID, and carrying the pool Job
// cache's managed-by label so the manager caches it. No termination message
// is read.
func Job(cfg pool.Config, mf *catalogv1alpha1.MediaFile, t grafttask.Task, name string) (*batchv1.Job, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	tmpl := pool.Template(&transcodev1alpha1.TranscodeProfile{}, transcodev1alpha1.HardwareCPU, cfg)
	c := &tmpl.Spec.Containers[0]
	c.Args = append([]string{"--data-dir", cmp.Or(cfg.DataDir, pool.DefaultDataDir), "--graft-task", string(b)}, cfg.ExtraArgs...)
	c.Env = slices.DeleteFunc(c.Env, func(e corev1.EnvVar) bool { return e.Name == pool.EnvProfileUID || e.Name == pool.EnvClass })
	tmpl.Labels[grafttask.LabelGraftOf] = string(mf.UID)
	tmpl.Labels["app.kubernetes.io/component"] = ComponentGraft
	ref := metav1.NewControllerRef(mf, catalogv1alpha1.GroupVersion.WithKind("MediaFile"))
	// The garbage collector removes the Job with its file; nothing waits
	// on the Job's deletion, so it needs no grant on mediafiles/finalizers.
	ref.BlockOwnerDeletion = nil
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: mf.Namespace,
			Labels: map[string]string{
				pool.LabelManagedBy: pool.ManagedByValue, grafttask.LabelGraftOf: string(mf.UID),
				"app.kubernetes.io/name": "clustarr", "app.kubernetes.io/component": ComponentGraft,
			},
			OwnerReferences: []metav1.OwnerReference{*ref},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            new(int32(0)),
			ActiveDeadlineSeconds:   new(int64(graftplan.JobDeadline.Seconds())),
			TTLSecondsAfterFinished: new(int32(jobTTL.Seconds())),
			Template:                tmpl,
		},
	}, nil
}
