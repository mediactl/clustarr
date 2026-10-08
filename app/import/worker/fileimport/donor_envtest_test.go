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

package fileimport_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/import/importplan"
	"github.com/mediactl/clustarr/app/import/worker/probe/probetest"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/mediainfo/native"
	"github.com/mediactl/clustarr/pkg/transcode/donoraudio"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
)

// donorTitle is a dual-audio release of the fixture's movie.
const donorTitle = "The.Matrix.1999.DVDRip.x264.AAC.DL-BoB"

// donorRun is an importRun of an audio donor for the fixture's movie, which
// lacks English; its original, the anchor a graft aligns on, is Japanese.
// recorded is the item's status.audio.donor.path.
func (f *fixture) donorRun(t *testing.T, uid, contentRoot, recorded string, now time.Time) *importRun {
	t.Helper()
	r := f.movieRun(t, uid, contentRoot, donorTitle, now)
	r.in.Entry.Purpose = commonv1.DownloadPurposeAudioDonor
	r.in.Donor = &importplan.DonorItem{
		Item: r.in.Owner, Missing: []string{"en"}, Original: "ja", AudioDefault: "ja", RecordedDonor: recorded,
	}
	return r
}

// dualAudio is a probe of a Japanese and English donor.
var dualAudio = probetest.ProberFunc(func(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	return &commonv1.MediaInfo{
		VideoCodec: "h264", Width: 720, Height: 480,
		Audio: []commonv1.AudioStream{{Language: "jpn"}, {Language: "eng"}},
	}, nil, nil
})

// donorFolder is the movie's donor folder under the fixture's root folder.
func (f *fixture) donorFolder(t *testing.T) string {
	t.Helper()
	var movie catalogv1alpha1.Movie
	require.NoError(t, f.api.Get(context.Background(), client.ObjectKey{Namespace: f.ns, Name: f.movieName}, &movie))
	return filepath.Join(f.mediaRoot, ".clustarr", "donors", string(movie.UID))
}

// cycle runs one inspect and, when the plan is approved, one execute, and
// returns the decision that reads the last record.
func (r *importRun) cycle() lifecycle.ImportDecision {
	r.t.Helper()
	dec := r.decide()
	require.NotNil(r.t, dec.Dispatch, "%+v", dec.Summary)
	require.Equal(r.t, schema.ImportSubInspect, dec.Dispatch.Phase, "%+v", dec.Summary)
	r.deliver()
	dec = r.decide()
	if dec.Dispatch == nil || dec.Dispatch.Phase != schema.ImportSubExecute {
		return dec
	}
	r.deliver()
	return r.decide()
}

// donorClip makes a two-second Matroska file with one H.264 video stream
// and one AAC track per language, tagged in that order, with the ffmpeg
// CLI; it skips without FFmpeg 9's libraries, which the probe and the
// reduction load in-process.
func donorClip(t *testing.T, dir string, langs ...string) string {
	t.Helper()
	if _, err := native.New(); err != nil {
		t.Skipf("no FFmpeg 9 with its shim for the probe and the reduction: %v", err)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg to make the donor clip")
	}
	out := filepath.Join(dir, donorTitle+".mkv")
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24:duration=2",
	}
	for i := range langs {
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:sample_rate=48000:duration=2", 440+110*i))
	}
	args = append(args, "-map", "0:v")
	for i := range langs {
		args = append(args, "-map", fmt.Sprintf("%d:a", i+1))
	}
	args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac")
	for i, l := range langs {
		args = append(args, fmt.Sprintf("-metadata:s:a:%d", i), "language="+l)
	}
	b, err := exec.Command("ffmpeg", append(args, out)...).CombinedOutput()
	require.NoError(t, err, "ffmpeg: %s", b)
	return out
}

// TestADonorIsReducedBesideTheLibraryAtImport (F7.1; loop spec §4.12, D8):
// a donor makes no MediaFile and no AudioGraft. The execute places it in the
// item's donor folder under the root folder and cuts it down to the anchor's
// and the missing languages' audio as <item>.mka at once -- its video leaves
// the library at import -- and the record names that .mka, which the item's
// key reads into status.audio.donor. The manager materialises nothing.
func TestADonorIsReducedBesideTheLibraryAtImport(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-donor")
	contentRoot := dataDir(t, "scratch")
	src := donorClip(t, contentRoot, "eng", "jpn", "ger")
	p, err := native.New()
	require.NoError(t, err)
	f.worker.Prober = p

	r := f.donorRun(t, "donor-entry", contentRoot, "", time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	dec := r.cycle()

	rec := r.record(schema.ImportSubExecute)
	require.NotNil(t, rec)
	require.NotNil(t, rec.Execute, rec.Failure)
	require.Empty(t, rec.Execute.Refused, rec.Failure)
	folder := f.donorFolder(t)
	want := filepath.Join(folder, f.movieName+".mka")
	assert.Equal(t, want, rec.Execute.DonorPath, "the reduced audio is the donor")
	assert.Empty(t, rec.Execute.Placed, "a donor is no library file")
	assert.NoFileExists(t, filepath.Join(folder, f.movieName+".mkv"), "the donor's video left the library at import")
	assert.FileExists(t, src, "the payload is the transfer's, which keeps seeding")
	tracks, err := engine.AudioTracks(want)
	require.NoError(t, err)
	var langs []string
	for _, tr := range tracks {
		langs = append(langs, tr.Language)
	}
	assert.ElementsMatch(t, []string{"jpn", "eng"}, langs, "the anchor and the missing language; the German dub is not kept")

	assert.Equal(t, lifecycle.ImportImported, dec.Verdict, dec.Summary.Message)
	assert.Empty(t, dec.Materialise, "no MediaFile is applied, and no earlier donor removed")
	assert.Zero(t, dec.Summary.Files, "the entry waits for no MediaFile")
	var files catalogv1alpha1.MediaFileList
	require.NoError(t, f.api.List(ctx, &files, client.InNamespace(f.ns)))
	assert.Empty(t, files.Items)
}

// TestADonorLackingALanguageAtReductionIsTheReleasesFault (F7.1): the
// reduction's own language check -- a donor whose tags the probe and the
// demuxer read differently -- is the release's fault, as the probe's is:
// inspected once more, then blocklisted.
func TestADonorLackingALanguageAtReductionIsTheReleasesFault(t *testing.T) {
	f := newFixture(t, "fi-donor-lacks")
	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, donorTitle+".mkv"), 1<<20)
	f.worker.Prober = dualAudio
	reductions := 0
	f.worker.ReduceDonor = func(context.Context, string, string, []string) (string, error) {
		reductions++
		return "", fmt.Errorf("%w: the donor needs tagged ja and en tracks; it has jpn", donoraudio.ErrLacksLanguage)
	}
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	r := f.donorRun(t, "donor-lacks-entry", contentRoot, "", now)

	dec := r.cycle()
	rec := r.record(schema.ImportSubExecute)
	require.NotNil(t, rec.Execute)
	assert.Equal(t, schema.ImportRefusedDonorLacksLanguage, rec.Execute.Refused)
	assert.Empty(t, rec.Execute.DonorPath)
	assert.Equal(t, commonv1.ImportClassReleaseFault, dec.Class)
	assert.Equal(t, lifecycle.ImportRetry, dec.Verdict, "a release fault is inspected once more first")
	assert.Contains(t, dec.Summary.Message, importplan.MessageEveryFileRejected)

	r.in.Now = now.Add(importplan.RetryLadder[0])
	dec = r.cycle()
	assert.Equal(t, 2, reductions)
	assert.Equal(t, lifecycle.ImportReleaseFault, dec.Verdict, "then the release is blocklisted")
	assert.Equal(t, catalogv1alpha1.ImportPhaseBlocked, dec.Summary.Phase)
	assert.Contains(t, dec.Summary.Message, importplan.MessageEveryFileRejected)
}

// TestADonorThatCannotBeReducedIsRetriedThenBlocked (F7.1, as A3.8 rules
// it): a reduction that fails for any reason but the release's is retried --
// at 1 m, 10 m and 45 m by the import summary's attempts, no longer by
// redelivery -- and the last failure holds the import for a person ("blocked"
// before A3.8): never the release's fault, so never blocklisted.
func TestADonorThatCannotBeReducedIsRetriedThenBlocked(t *testing.T) {
	f := newFixture(t, "fi-donor-reduce")
	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, donorTitle+".mkv"), 1<<20)
	f.worker.Prober = dualAudio
	reductions := 0
	f.worker.ReduceDonor = func(context.Context, string, string, []string) (string, error) {
		reductions++
		return "", errors.New("libavformat: I/O error")
	}
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	r := f.donorRun(t, "donor-reduce-entry", contentRoot, "", now)

	for attempt, wait := range importplan.RetryLadder {
		dec := r.cycle()
		rec := r.record(schema.ImportSubExecute)
		require.NotNil(t, rec)
		assert.Contains(t, rec.Failure, "I/O error")
		assert.Equal(t, commonv1.ImportClassTransient, dec.Class, "attempt %d", attempt+1)
		require.Equal(t, lifecycle.ImportRetry, dec.Verdict, "attempt %d: %s", attempt+1, dec.Summary.Message)
		require.NotNil(t, dec.Summary.NextAttemptAt)
		assert.Equal(t, r.in.Now.Add(wait), dec.Summary.NextAttemptAt.UTC(), "attempt %d retries after %s", attempt+1, wait)
		assert.EqualValues(t, attempt+1, dec.Summary.Attempts)
		r.in.Now = dec.Summary.NextAttemptAt.UTC()
	}
	dec := r.cycle()
	assert.Equal(t, len(importplan.RetryLadder)+1, reductions, "a first try and three retries")
	assert.Equal(t, lifecycle.ImportHeld, dec.Verdict, dec.Summary.Message)
	assert.Equal(t, catalogv1alpha1.ImportPhaseHeld, dec.Summary.Phase)
	assert.NotEqual(t, lifecycle.ImportReleaseFault, dec.Verdict, "not the release's fault: never blocklisted")
	assert.NotContains(t, dec.Summary.Message, importplan.MessageEveryFileRejected)
	assert.Contains(t, dec.Summary.Message, "I/O error")
}

// TestANewDonorRemovesTheItemsRecordedOne (F7.1, §2.14): the item's
// status.audio.donor names the donor a graft reads; a newer donor's import
// places and reduces the new one, and the manager removes the recorded one
// from disk with the audio it was reduced to -- never the file just placed.
// The removal is the manager's effect (fsops.SafeRemove under the data
// root), run here as its executor runs it.
func TestANewDonorRemovesTheItemsRecordedOne(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-donor-replace")
	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, donorTitle+".mkv"), 1<<20)
	f.worker.Prober = dualAudio
	folder := f.donorFolder(t)
	require.NoError(t, os.MkdirAll(folder, 0o755))
	older := filepath.Join(folder, "older.mkv")
	olderAudio := filepath.Join(folder, "older.mka")
	for _, p := range []string{older, olderAudio} {
		require.NoError(t, os.WriteFile(p, []byte("old donor"), 0o644))
	}
	reduced := filepath.Join(folder, f.movieName+".mka")
	f.worker.ReduceDonor = func(_ context.Context, src, anchor string, langs []string) (string, error) {
		assert.Equal(t, "ja", anchor)
		assert.Equal(t, []string{"en"}, langs)
		if err := os.WriteFile(reduced, []byte("reduced"), 0o644); err != nil {
			return "", err
		}
		return reduced, os.Remove(src)
	}

	r := f.donorRun(t, "donor-replace-entry", contentRoot, older, time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	dec := r.cycle()
	require.Equal(t, lifecycle.ImportImported, dec.Verdict, dec.Summary.Message)
	assert.FileExists(t, older, "the agent removes nothing the plan did not place")

	var gone []string
	for _, m := range dec.Materialise {
		require.Nil(t, m.Apply, "a donor applies no MediaFile")
		require.Nil(t, m.Delete)
		gone = append(gone, m.Remove...)
	}
	assert.ElementsMatch(t, []string{older, olderAudio}, gone, "the superseded donor and its reduced audio")
	assert.NotContains(t, gone, reduced, "never the donor just placed")
	dataRoot := filepath.Dir(filepath.Dir(f.mediaRoot)) // /data, the manager's data root
	require.True(t, strings.HasPrefix(f.mediaRoot, dataRoot+"/"))
	for _, p := range gone {
		require.NoError(t, fsops.SafeRemove(ctx, dataRoot, p))
	}
	assert.NoFileExists(t, older, "the superseded donor is removed")
	assert.NoFileExists(t, olderAudio)
	assert.FileExists(t, reduced)
}
