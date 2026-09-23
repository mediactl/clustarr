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

package worker

import (
	"errors"
	"fmt"
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/squasharr/task"
)

// ErrNoRootFolder is a source, or an output that is not in place, lying under
// no RootFolder. The worker never touches a file outside one.
var ErrNoRootFolder = errors.New("not under any RootFolder")

// ErrInvalidOutput is an output path OutputPath refuses.
var ErrInvalidOutput = errors.New("invalid output path")

// BuildTask renders one dispatch of tj for the class it goes to: everything
// the worker used to read from the apiserver, resolved by the one function
// the controller and the worker's tests share (spec §6, §17.5).
func BuildTask(tj *transcodev1alpha1.TranscodeJob, tp *transcodev1alpha1.TranscodeProfile,
	mf *catalogv1alpha1.MediaFile, folders []catalogv1alpha1.RootFolder,
	attempt int32, class transcodev1alpha1.Hardware,
) (task.Task, error) {
	source := tj.Spec.SourcePath
	if source == "" {
		source = mf.Spec.Path
	}
	source = filepath.Clean(source)
	rf := rootFolderFor(folders, source)
	if rf == nil {
		return task.Task{}, fmt.Errorf("source %s: %w", source, ErrNoRootFolder)
	}
	// OutputPath needs the SourcePath to be set, so make a copy with it populated
	spec := tj.Spec
	spec.SourcePath = source
	out, err := OutputPath(spec, tp.Name, tp.Spec.Container, ReplaceSource(tp.Spec.Policy))
	if err != nil {
		return task.Task{}, fmt.Errorf("%w: %w", ErrInvalidOutput, err)
	}
	bin := rf.Spec.RecycleBin.Path
	if bin == "" {
		bin = defaultRecycleBin
	}
	t := task.Task{
		Job:     schema.Ref{Namespace: tj.Namespace, Name: tj.Name, UID: string(tj.UID)},
		Attempt: attempt,
		Class:   string(class),
		Profile: task.Profile{
			Name: tp.Name, Hash: tp.Status.Hash, Spec: *tp.Spec.DeepCopy(), Hardware: ptr.To(class),
		},
		SourcePath:      source,
		SourceProbeHash: tj.Spec.SourceProbeHash,
		SourceSizeBytes: mf.Spec.SizeBytes,
		SourceModifier:  string(mf.Spec.Quality.Modifier),
		OutputPath:      out,
		Root:            task.RootFolder{Path: rf.Spec.Path, RecycleBin: bin},
		Deadline:        metav1.Duration{Duration: ActiveDeadline(tp.Spec)},
	}
	if tj.Status.Plan != nil {
		t.ArgsHash = tj.Status.Plan.ArgsHash
	}
	if filepath.Clean(out) != source {
		orf := rootFolderFor(folders, out)
		if orf == nil {
			return task.Task{}, fmt.Errorf("output %s: %w", out, ErrNoRootFolder)
		}
		t.OutputRoot = orf.Spec.Path
	}
	return t, nil
}
