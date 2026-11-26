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
	"k8s.io/apimachinery/pkg/api/resource"
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

// Resources is what a graft Job's pod asks the scheduler for and may use
// (owner, 2026-11-26: "It definitely should not be 8. And memory - does it
// need to be 4Gi?"). A graft decodes two audio tracks to 8 kHz mono, aligns
// them (pkg/audioalign) and muxes with every stream copied: no picture is
// encoded. It took the cpu pool's template whole, and with that the
// resources of a profile that states none -- limits of 8 CPUs and 4Gi,
// sized for x265, and no requests, which the apiserver then sets to the
// limits. Each graft asked for 8 whole CPUs, and on talos no node had 8
// unrequested: two grafts stood Pending from 13:19Z until their two-hour
// deadline, and their successors after them.
//
// Measured on the alignment alone (2026-11-26, synthetic programme, this
// package's record in docs/claude/transcoding.md): a 24-minute episode is
// 30 s of CPU and 0.41 GiB at its peak; a 150-minute film is 24 minutes of
// CPU and 2.0 GiB.
// Two threads finish the episode in 17 s; eight take 16 s and three times
// the CPU, because every window's transform spans the whole programme and
// the threads contend for memory, not for cores.
//
// So the request is what an episode's graft uses, the CPU limit is the two
// threads past which there is nothing to gain, and the memory limit stays
// where a film needs it: a limit reserves nothing on a node.
func Resources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		},
	}
}

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
	// The template's CLUSTARR_CPU_LIMIT reads limits.cpu through the
	// Downward API, so it follows these.
	c.Resources = Resources()
	c.Args = append([]string{"--data-dir", cmp.Or(cfg.DataDir, pool.DefaultDataDir), "--graft-task", string(b)}, cfg.ExtraArgs...)
	c.Env = slices.DeleteFunc(c.Env, func(e corev1.EnvVar) bool { return e.Name == pool.EnvProfileUID || e.Name == pool.EnvClass })
	tmpl.Labels[grafttask.LabelGraftOf] = string(mf.UID)
	tmpl.Labels["app.kubernetes.io/component"] = ComponentGraft
	ref := metav1.NewControllerRef(mf, catalogv1alpha1.GroupVersion.WithKind("MediaFile"))
	// The reference is how a Job's event finds its file's key
	// (remediation.GraftJobKeys). It no longer has the Job collected with
	// its file: a MediaFile is a row in the library store (ADR-0024), the
	// apiserver's garbage collector cannot resolve the owner and leaves the
	// Job be, and the loop deletes it at the pass the file's delete wakes
	// (Reconciler.forgetGraftJobs). Nothing blocks on the owner either way.
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
