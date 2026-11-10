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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// A manual assignment keeps what the file says (2026-11-10 design D6; C5):
// the person named the album, and the file's tags, else its name, say which
// of the selected release's tracks it is, read off the probe the assignment
// used to run and discard -- so the MediaFile backs the album as that track,
// never as no track; a file naming none is held in status.unmatched with the
// reason and the album as its candidate, and nothing is recorded.
func TestHandleManualAssignmentAttributesTheAlbumsTrack(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-assign-track", catalogv1alpha1.RootFolderKindMusic, "", catalogv1alpha1.ScanModeFull)
	album := createArtistAlbum(t, ctx, f)

	byName := filepath.Join("Misc", "ok", "02 - Paranoid Android.flac")
	byTags := filepath.Join("Misc", "ok", "untitled.flac")
	stray := filepath.Join("Misc", "ok", "bonus material.flac")
	for _, rel := range []string{byName, byTags, stray} {
		mustWriteFile(t, filepath.Join(f.root, rel), smallFile)
	}
	worker := func() *rescan.Worker {
		w := rescan.NewWorker(f.c, f.bus, nil)
		w.ProbeAudio = func(_ context.Context, path string) (mediainfo.AudioProbe, error) {
			ap := mediainfo.AudioProbe{Codec: "flac", SampleBits: 16}
			if filepath.Base(path) == filepath.Base(byTags) {
				ap.Tags = &commonv1.MediaTags{Album: "OK Computer", DiscNumber: 1, TrackNumber: 3}
			}
			return ap, nil
		}
		return w
	}

	scan, msg := f.assignScan(t, ctx, "assign-name", "album/"+album.Name, byName)
	require.NoError(t, worker().Handle(ctx, msg))
	got := readProgress(t, ctx, f.bus, string(scan.UID))
	require.Empty(t, got.Error)
	assert.Equal(t, int64(1), got.FilesMatched)

	scan, msg = f.assignScan(t, ctx, "assign-tags", "album/"+album.Name, byTags)
	require.NoError(t, worker().Handle(ctx, msg))
	got = readProgress(t, ctx, f.bus, string(scan.UID))
	require.Empty(t, got.Error)
	assert.Equal(t, int64(1), got.FilesMatched)

	tracks := map[string]string{}
	for _, mf := range mediaFilesIn(t, ctx, f.c, f.ns, 2) {
		assert.Equal(t, commonv1.MediaKindAlbum, mf.Spec.MediaRef.Kind)
		assert.Equal(t, album.Name, mf.Spec.MediaRef.Name)
		require.NotNil(t, mf.Spec.ImportedFrom)
		assert.True(t, mf.Spec.ImportedFrom.Manual)
		tracks[filepath.Base(mf.Spec.Path)] = mf.Spec.MediaRef.Track
		assert.Equal(t, "FLAC", mf.Spec.Quality.Name, "the probe the attribution read also froze the quality")
	}
	assert.Equal(t, map[string]string{
		filepath.Base(byName): "paranoid-android", // the name's number
		filepath.Base(byTags): "exit-music",       // the tags' disc and track
	}, tracks)

	scan, msg = f.assignScan(t, ctx, "assign-stray", "album/"+album.Name, stray)
	require.NoError(t, worker().Handle(ctx, msg))
	got = readProgress(t, ctx, f.bus, string(scan.UID))
	require.Empty(t, got.Error, "a file naming no track is the scan's unmatched, not its failure")
	assert.Zero(t, got.FilesMatched)
	u := unmatchedByPath(got)[stray]
	assert.Contains(t, u.Reason, "names no track of the selected release (3 tracks on 1 media)")
	assert.Equal(t, []string{album.Name}, u.Candidates, "the album is known; the track is not")
	var files catalogv1alpha1.MediaFileList
	require.NoError(t, f.c.List(ctx, &files, client.InNamespace(f.ns)))
	assert.Len(t, files.Items, 2, "the stray backs the album as no track of it")
}

// An audiobook part assigned by hand is ordered as a scanned one is
// (2026-11-10 design D11; C5): spec.partIndex from the tags' disc and track,
// else the name's part number, read off the probe the assignment keeps.
func TestHandleManualAssignmentOrdersTheAudiobookPart(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-assign-part", catalogv1alpha1.RootFolderKindAudiobook, "", catalogv1alpha1.ScanModeFull)
	ab := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "left-hand-of-darkness", Namespace: f.ns},
		Spec:       catalogv1alpha1.AudiobookSpec{ASIN: "B002V1QK4D", QualityProfileRef: "audiobooks", RootFolderRef: f.rf.Name},
	}
	require.NoError(t, f.c.Create(ctx, ab))
	waitCached(t, ctx, f.c, ab, func() bool { return true })

	byName := filepath.Join("Misc", "lhod", "The Left Hand of Darkness - Part 03.mp3")
	byTags := filepath.Join("Misc", "lhod", "tagged.mp3")
	for _, rel := range []string{byName, byTags} {
		mustWriteFile(t, filepath.Join(f.root, rel), smallFile)
	}
	for i, rel := range []string{byName, byTags} {
		w := rescan.NewWorker(f.c, f.bus, nil)
		w.ProbeAudio = func(_ context.Context, path string) (mediainfo.AudioProbe, error) {
			ap := mediainfo.AudioProbe{Codec: "mp3", BitrateKbps: 64}
			if filepath.Base(path) == filepath.Base(byTags) {
				ap.Tags = &commonv1.MediaTags{Album: "The Left Hand of Darkness", DiscNumber: 2, TrackNumber: 1}
			}
			return ap, nil
		}
		scan, msg := f.assignScan(t, ctx, []string{"assign-by-name", "assign-by-tags"}[i], "audiobook/"+ab.Name, rel)
		require.NoError(t, w.Handle(ctx, msg))
		got := readProgress(t, ctx, f.bus, string(scan.UID))
		require.Empty(t, got.Error)
		assert.Equal(t, int64(1), got.FilesMatched, rel)
	}

	parts := map[string]int32{}
	for _, mf := range mediaFilesIn(t, ctx, f.c, f.ns, 2) {
		assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: ab.Name}, mf.Spec.MediaRef)
		require.NotNil(t, mf.Spec.ImportedFrom)
		assert.True(t, mf.Spec.ImportedFrom.Manual)
		parts[filepath.Base(mf.Spec.Path)] = mf.Spec.PartIndex
	}
	assert.Equal(t, map[string]int32{filepath.Base(byName): 3, filepath.Base(byTags): 2001}, parts,
		"the name's part number; disc 2 track 1 from the tags")
}
