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

package rename

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Under a RootFolder with naming.renameTranscoded, a file squasharr
// transcoded (status.transcode.profileTag) is renamed to its canonical
// file name in its own folder, so "[EAC3 5.1][h264]" no longer describes
// an HEVC file; a file nobody transcoded keeps its name, since renameFiles
// is off.
func TestATranscodedFileIsRenamedInItsOwnFolder(t *testing.T) {
	ctx := context.Background()
	lib := t.TempDir()
	season := filepath.Join(lib, "Bluey (2018) {tvdb-353546}", "Season 3")
	require.NoError(t, os.MkdirAll(season, 0o755))

	file := func(name, base, canonical string, transcoded bool) *catalogv1alpha1.MediaFile {
		path := filepath.Join(season, base)
		require.NoError(t, os.WriteFile(path, []byte(name), 0o644))
		info, err := os.Stat(path)
		require.NoError(t, err)
		mf := &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
			Spec: catalogv1alpha1.MediaFileSpec{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: name},
				Path:     path, SizeBytes: info.Size(), ModTime: metav1.NewTime(info.ModTime().Truncate(time.Second)),
				Quality: commonv1.Quality{Name: "WEBDL-1080p", Source: commonv1.SourceWebDL, Resolution: 1080},
			},
			Status: catalogv1alpha1.MediaFileStatus{
				Naming: &catalogv1alpha1.NamingStatus{ExpectedPath: filepath.Join(lib, "Bluey (2018) {tvdb-353546}", "Season 03", canonical)},
				Conditions: []metav1.Condition{
					{Type: catalogv1alpha1.ConditionNamingCurrent, Status: metav1.ConditionFalse, Reason: "Stale", LastTransitionTime: metav1.Now()},
					{Type: catalogv1alpha1.MediaFileConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.Now()},
					{Type: catalogv1alpha1.MediaFileConditionProbed, Status: metav1.ConditionTrue, Reason: "Probed", LastTransitionTime: metav1.Now()},
				},
			},
		}
		if transcoded {
			mf.Status.Transcode = &catalogv1alpha1.TranscodeState{ProfileTag: "hevc-mkv@f0a6f12c", Compliant: true}
		}
		return mf
	}
	done := file("s03e09", "Bluey (2018) - S03E09 - Curry Quest [WEBDL-1080p][EAC3 5.1][h264]-NTb.mkv",
		"Bluey (2018) - S03E09 - Curry Quest [WEBDL-1080p]-NTb.mkv", true)
	untouched := file("s03e20", "Bluey (2018) - S03E20 - Driving [WEBDL-1080p][EAC3 5.1][h264]-NTb.mkv",
		"Bluey (2018) - S03E20 - Driving [WEBDL-1080p]-NTb.mkv", false)
	// A transcoded file whose status.transcode was cleared (a rename read as
	// a change of its bytes, before bytesChanged) still reads spec.original
	// false, which catalogarr sets at the swap and never resets.
	lostRecord := file("s03e21", "Bluey (2018) - S03E21 - Bob Bilby [WEBDL-1080p][EAC3 5.1][h264]-NTb.mkv",
		"Bluey (2018) - S03E21 - Bob Bilby [WEBDL-1080p]-NTb.mkv", false)
	lostRecord.Spec.Original = new(false)

	root := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "tv", Namespace: "media"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: lib, Naming: catalogv1alpha1.NamingSpec{RenameTranscoded: true}},
	}
	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "bluey", Namespace: "media"},
		Spec:       catalogv1alpha1.SeriesSpec{RootFolderRef: "tv"},
	}
	ep := func(name string) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "bluey"},
		}
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithStatusSubresource(&catalogv1alpha1.MediaFile{}).
		WithObjects(root, series, ep("s03e09"), ep("s03e20"), ep("s03e21"), done, untouched, lostRecord).Build()
	r := &Reconciler{Client: c, APIReader: c}

	for _, name := range []string{"s03e09", "s03e20", "s03e21"} {
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "media", Name: name}})
		require.NoError(t, err)
	}
	assert.FileExists(t, filepath.Join(season, "Bluey (2018) - S03E09 - Curry Quest [WEBDL-1080p]-NTb.mkv"),
		"the transcoded file carries its canonical name, in Season 3")
	assert.NoFileExists(t, done.Spec.Path)
	assert.FileExists(t, untouched.Spec.Path, "a file nobody transcoded keeps its name")
	assert.FileExists(t, filepath.Join(season, "Bluey (2018) - S03E21 - Bob Bilby [WEBDL-1080p]-NTb.mkv"),
		"spec.original false is a transcode too")
}

// A finished transcode wakes the rename: with a template that names no
// codec (the owner's TV override), the swap changes neither the proposed
// path nor the conditions of a file already out of name -- only
// status.transcode.
func TestATranscodeWakesTheRename(t *testing.T) {
	mf := &catalogv1alpha1.MediaFile{
		Status: catalogv1alpha1.MediaFileStatus{
			Naming: &catalogv1alpha1.NamingStatus{ExpectedPath: "/data/tv/S/Season 03/S - S01E01.mkv"},
			Conditions: []metav1.Condition{
				{Type: catalogv1alpha1.ConditionNamingCurrent, Status: metav1.ConditionFalse},
				{Type: catalogv1alpha1.MediaFileConditionReady, Status: metav1.ConditionTrue},
				{Type: catalogv1alpha1.MediaFileConditionProbed, Status: metav1.ConditionTrue},
			},
		},
	}
	swapped := mf.DeepCopy()
	swapped.Status.Transcode = &catalogv1alpha1.TranscodeState{ProfileTag: "hevc-mkv@f0a6f12c"}
	assert.True(t, Predicate().Update(event.UpdateEvent{ObjectOld: mf, ObjectNew: swapped}))
	assert.False(t, Predicate().Update(event.UpdateEvent{ObjectOld: swapped, ObjectNew: swapped.DeepCopy()}), "nothing changed")
}
