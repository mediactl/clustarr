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

package rescan_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/rescan"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/squasharr/worker"
)

// keptOutputName reads back exactly the names squasharr's OutputPath gives
// a replaceSource=false output -- the convention is restated in importarr,
// so it is held to squasharr's own function here -- and nothing a person
// would call a version of their own.
func TestKeptOutputNameReadsSquasharrsName(t *testing.T) {
	for _, tc := range []struct {
		source, profile string
		container       transcodev1alpha1.Container
	}{
		{"/data/media/movies/Heat (1995)/Heat (1995) - Bluray-1080p.mkv", "hevc-1080p", transcodev1alpha1.ContainerMKV},
		{"/data/media/movies/Heat (1995)/Heat (1995).MP4", "hevc.small", transcodev1alpha1.ContainerMKV},
		{"/data/media/tv/Show/Season 01/Show - S01E01 - Pilot.avi", "mp4out", transcodev1alpha1.ContainerMP4},
	} {
		out, err := worker.OutputPath(transcodev1alpha1.TranscodeJobSpec{SourcePath: tc.source}, tc.profile, tc.container, false)
		require.NoError(t, err)
		stem, profile, ok := rescan.KeptOutputName(filepath.Base(out))
		require.True(t, ok, "%s is squasharr's name", out)
		assert.Equal(t, tc.profile, profile)
		base := filepath.Base(tc.source)
		assert.Equal(t, base[:len(base)-len(filepath.Ext(base))], stem, "the kept source's stem")
	}
	for _, base := range []string{
		"Heat (1995) - Director's Cut.mkv", // not a profile name
		"Heat (1995) - HEVC.mkv",           // profile names are lower-case
		"Heat (1995) - hevc-1080p.avi",     // squasharr writes mkv or mp4
		"Heat (1995).mkv",                  // no label
		" - hevc.mkv",                      // no stem
	} {
		_, _, ok := rescan.KeptOutputName(base)
		assert.False(t, ok, "%q is not squasharr's name", base)
	}
}

// A replaceSource=false output stays protected after its TranscodeJob is
// gone: named beside the recorded kept source, and confirmed by squasharr's
// record -- the source's status.transcode.profileTag, with no probe -- or,
// when that record has moved on, by the file's own CLUSTARR_PROFILE. A file
// so named that is not squasharr's is scanned like any other; one whose
// tag cannot be read is reported, never guessed at.
func TestHandleKeepsAKeptSourcesOutputOnceItsJobIsGone(t *testing.T) {
	for i, tc := range []struct {
		name string
		// sourceTag is the kept source's status.transcode.profileTag.
		sourceTag string
		// probe is what the output's own CLUSTARR_PROFILE reads as.
		probe    string
		probeErr error
		// kept: skipped as a transcode output; adopted: a second MediaFile;
		// unmatched: reported with CodeUnconfirmedTranscodeOutput.
		kept, adopted, unmatched bool
		probed                   bool
	}{
		{name: "the kept source records the profile", sourceTag: "hevc-1080p@abc", kept: true},
		{name: "the file's own tag names the profile", sourceTag: "other@x", probe: "hevc-1080p@abc", kept: true, probed: true},
		{name: "no tag: a file of the item's own", probe: "", adopted: true, probed: true},
		{name: "another profile's tag", probe: "other@x", adopted: true, probed: true},
		{name: "the tag cannot be read", probeErr: errors.New("ffprobe: invalid data"), unmatched: true, probed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t, ctx, fmt.Sprintf("rw-kept-%d", i), catalogv1alpha1.RootFolderKindMovie,
				"hd-bluray-web", catalogv1alpha1.ScanModeIncremental)
			name, source, _ := f.importedFile(t, ctx) // Heat, recorded, no TranscodeJob anywhere
			f.waitOriginal(t, ctx, name, true)
			if tc.sourceTag != "" {
				_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarr, catalogac.MediaFile(name, f.ns).WithStatus(
					catalogac.MediaFileStatus().WithTranscode(catalogac.TranscodeState().
						WithProfileTag(tc.sourceTag).WithLastResult(catalogv1alpha1.TranscodeResultSucceeded))))
				require.NoError(t, err)
				waitFor(t, 10*time.Second, func() bool {
					var mf catalogv1alpha1.MediaFile
					return f.c.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: name}, &mf) == nil &&
						mf.Status.Transcode != nil && mf.Status.Transcode.ProfileTag == tc.sourceTag
				})
			}
			kept := source[:len(source)-len(".mkv")] + " - hevc-1080p.mkv"
			mustWriteFile(t, kept, sampleFloor)

			var probes atomic.Int64
			w := rescan.NewWorker(f.c, f.bus)
			w.ProbeTranscodeProfile = func(_ context.Context, path string) (string, error) {
				probes.Add(1)
				assert.Equal(t, kept, path)
				return tc.probe, tc.probeErr
			}
			require.NoError(t, w.Handle(ctx, newFakeMessage(t, f.task(false))))
			got := readProgress(t, ctx, f.bus, string(f.scan.UID))
			require.Empty(t, got.Error)
			assert.Equal(t, tc.probed, probes.Load() > 0, "the probe runs only when the source's record does not confirm")
			assert.Equal(t, int64(1), got.Unchanged, "the recorded source")

			switch {
			case tc.kept:
				assert.Equal(t, int64(1), got.TranscodeOutputs)
				assert.Empty(t, got.Unmatched)
				mediaFilesIn(t, ctx, f.c, f.ns, 1)
			case tc.adopted:
				assert.Zero(t, got.TranscodeOutputs)
				assert.Equal(t, int64(1), got.FilesMatched, "scanned like any other file of the movie")
				mediaFilesIn(t, ctx, f.c, f.ns, 2)
			case tc.unmatched:
				assert.Zero(t, got.TranscodeOutputs)
				u, ok := unmatchedByPath(got)[filepath.Base(filepath.Dir(kept))+"/"+filepath.Base(kept)]
				require.True(t, ok, "reported unmatched: %+v", got.Unmatched)
				assert.Contains(t, u.Reason, "CLUSTARR_PROFILE tag could not be read")
				assert.Contains(t, u.Reason, "invalid data")
				mediaFilesIn(t, ctx, f.c, f.ns, 1)
			}
		})
	}
}
