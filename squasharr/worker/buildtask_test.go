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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

func folder(name, path, bin string) catalogv1alpha1.RootFolder {
	rf := catalogv1alpha1.RootFolder{ObjectMeta: metav1.ObjectMeta{Name: name}}
	rf.Spec.Path = path
	rf.Spec.RecycleBin.Path = bin
	return rf
}

func TestBuildTask(t *testing.T) {
	folders := []catalogv1alpha1.RootFolder{
		folder("all", "/data/media", ""),
		folder("movies", "/data/media/movies", "/data/media/.bin"),
	}
	tp := &transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "hevc", UID: "puid"}}
	tp.Status.Hash = "abc123"
	tp.Spec.Container = transcodev1alpha1.Container("mkv")
	tp.Spec.ActiveDeadline = metav1.Duration{Duration: 3 * time.Hour}
	mf := &catalogv1alpha1.MediaFile{}
	mf.Spec.Path = "/data/media/movies/Heat (1995)/Heat.mkv"
	mf.Spec.SizeBytes = 42
	tj := &transcodev1alpha1.TranscodeJob{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tj", UID: "juid"}}
	tj.Spec.SourceProbeHash = "ph"
	tj.Status.Plan = &transcodev1alpha1.Plan{ArgsHash: "ah"}

	got, err := BuildTask(tj, tp, mf, folders, 3, transcodev1alpha1.HardwareNVIDIA)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/movies", got.Root.Path, "the deepest containing folder wins")
	assert.Equal(t, "/data/media/.bin", got.Root.RecycleBin)
	assert.Equal(t, mf.Spec.Path, got.SourcePath, "an empty spec.sourcePath falls back to the MediaFile")
	assert.Equal(t, mf.Spec.Path, got.OutputPath, "same container, replaceSource defaulted: in place")
	assert.Empty(t, got.OutputRoot)
	assert.Equal(t, int32(3), got.Attempt)
	assert.Equal(t, "nvidia", got.Class)
	assert.Equal(t, "ah", got.ArgsHash)
	assert.Equal(t, int64(42), got.SourceSizeBytes)
	assert.Equal(t, 3*time.Hour, got.Deadline.Duration)
	assert.Equal(t, "juid", got.Job.UID)
	assert.Equal(t, transcodev1alpha1.HardwareNVIDIA, *got.Profile.Hardware)

	folders[1].Spec.RecycleBin.Path = ""
	got, err = BuildTask(tj, tp, mf, folders, 1, transcodev1alpha1.HardwareCPU)
	require.NoError(t, err)
	assert.Equal(t, defaultRecycleBin, got.Root.RecycleBin)

	elsewhere := tj.DeepCopy()
	elsewhere.Spec.OutputPath = ptr.To("/data/media/other/Heat.mkv")
	got, err = BuildTask(elsewhere, tp, mf, folders, 1, transcodev1alpha1.HardwareCPU)
	require.NoError(t, err)
	assert.Equal(t, "/data/media", got.OutputRoot)

	outside := tj.DeepCopy()
	outside.Spec.OutputPath = ptr.To("/tmp/Heat.mkv")
	_, err = BuildTask(outside, tp, mf, folders, 1, transcodev1alpha1.HardwareCPU)
	assert.ErrorIs(t, err, ErrNoRootFolder)

	stray := mf.DeepCopy()
	stray.Spec.Path = "/srv/Heat.mkv"
	_, err = BuildTask(tj, tp, stray, folders, 1, transcodev1alpha1.HardwareCPU)
	assert.ErrorIs(t, err, ErrNoRootFolder)

	tp.Spec.ActiveDeadline = metav1.Duration{}
	got, err = BuildTask(tj, tp, mf, folders, 1, transcodev1alpha1.HardwareCPU)
	require.NoError(t, err)
	assert.Equal(t, DefaultActiveDeadline, got.Deadline.Duration)
}
