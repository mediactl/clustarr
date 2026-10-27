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

package jobspec

import (
	"errors"
	"fmt"
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// ErrNoRootFolder is a source, or an output that is not in place, lying under
// no RootFolder. The worker never touches a file outside one.
var ErrNoRootFolder = errors.New("not under any RootFolder")

// ErrInvalidOutput is an output path OutputPath refuses.
var ErrInvalidOutput = errors.New("invalid output path")

// BuildTask renders dispatch seq of mf's transcode under tp (hash is
// ProfileHash, the block's profileHash) for class, carrying the planned
// plan's hash: everything the worker reads, resolved by the one function the
// transcode planner and the worker's tests share (loop spec §4.12, §5.8).
// mf is the view's file with Status set to the draft, so its ProbeHash is
// the one planned.
func BuildTask(mf *catalogv1alpha1.MediaFile, tp *transcodev1alpha1.TranscodeProfile, hash string,
	folders []catalogv1alpha1.RootFolder, seq int64, class transcodev1alpha1.Hardware, plan *catalogv1alpha1.TranscodePlan,
) (task.Task, error) {
	source := filepath.Clean(mf.Spec.Path)
	rf := RootFolderFor(folders, source)
	if rf == nil {
		return task.Task{}, fmt.Errorf("source %s: %w", source, ErrNoRootFolder)
	}
	// The plan's container: MP4, or Matroska when it keeps image subtitles
	// (standard.Version 7). The worker refuses a task whose output is not in
	// its own plan's container, so the two must agree here.
	container := OutputContainer
	if plan != nil && plan.Container != "" {
		container = transcodev1alpha1.Container(plan.Container)
	}
	out, err := OutputPath(source, tp.Name, container, ReplaceSource(tp.Spec.Policy))
	if err != nil {
		return task.Task{}, fmt.Errorf("%w: %w", ErrInvalidOutput, err)
	}
	t := task.Task{
		File: schema.Ref{Namespace: mf.Namespace, Name: mf.Name, UID: string(mf.UID)}, Seq: seq, Class: string(class),
		Profile:    task.Profile{Name: tp.Name, Hash: hash, Spec: *tp.Spec.DeepCopy(), Hardware: new(class)},
		SourcePath: source, SourceProbeHash: mf.Status.ProbeHash, SourceSizeBytes: mf.Spec.SizeBytes,
		OutputPath: out,
		Root:       task.RootFolder{Path: rf.Spec.Path, RecycleBin: RecycleBinOf(rf)},
		Deadline:   metav1.Duration{Duration: ActiveDeadline(tp.Spec)}, Engine: task.EngineFFgo,
	}
	if plan != nil {
		t.PlanHash = plan.PlanHash
	}
	if filepath.Clean(out) != source {
		orf := RootFolderFor(folders, out)
		if orf == nil {
			return task.Task{}, fmt.Errorf("output %s: %w", out, ErrNoRootFolder)
		}
		t.OutputRoot = orf.Spec.Path
	}
	t.Inputs = task.Inputs{
		ProfileHash: hash, PlanHash: t.PlanHash, Class: string(class), SourcePath: source,
		ProbeHash: mf.Status.ProbeHash, OutputPath: out,
	}
	return t, nil
}
