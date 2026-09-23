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
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/rescan"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// scoringProfile creates a minimal, valid, cluster-scoped video
// QualityProfile named name. hd-bluray-tier-01 is an ungrouped catalogue
// format -- a 1080p BluRay encode by one of TRaSH's tier-01 groups -- so
// every video profile scores it at its default.
func scoringProfile(t *testing.T, ctx context.Context, c client.Client, name string) *catalogv1alpha1.QualityProfile {
	t.Helper()
	qp := &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
			Cutoff:    "hd",
			Tiers:     []catalogv1alpha1.Tier{{Name: "hd", Qualities: []string{"Bluray-1080p", "WEBDL-1080p"}}},
		},
	}
	require.NoError(t, c.Create(ctx, qp))
	waitFor(t, 10*time.Second, func() bool {
		var got catalogv1alpha1.QualityProfile
		return c.Get(ctx, client.ObjectKey{Name: name}, &got) == nil
	})
	return qp
}

// The carried "every scanned file reads as score 0": a scanned movie file
// is scored against its movie's QualityProfile exactly as an imported one
// is, and freezes the score, the formats that matched and the profile hash
// it was scored against.
func TestHandleScoresAScannedMovieFileAgainstItsProfile(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	qp := scoringProfile(t, ctx, c, "qp-rw-score")
	f := newFixture(t, ctx, "rw-score", catalogv1alpha1.RootFolderKindMovie, qp.Name, catalogv1alpha1.ScanModeFull)

	path := filepath.Join(f.root, "Heat (1995) [tmdbid-949]", "Heat.1995.1080p.BluRay.x264-CtrlHD [tmdbid-949].mkv")
	mustWriteFile(t, path, sampleFloor)

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))
	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.Empty(t, got.Unmatched)
	require.Equal(t, int64(1), got.FilesMatched)

	resolved, errs := quality.FromCRD(qp, catalogue.LoadedCatalogue())
	require.Empty(t, errs)

	mf := mediaFilesIn(t, ctx, f.c, f.ns, 1)[0]
	assert.Contains(t, mf.Spec.MatchedFormats, "hd-bluray-tier-01")
	assert.Equal(t, int32(resolved.Scores["hd-bluray-tier-01"]), mf.Spec.FormatScore)
	assert.NotZero(t, mf.Spec.FormatScore)
	assert.Equal(t, resolved.Hash, mf.Spec.ProfileHash)
	for _, leaf := range []string{"spec.formatScore", "spec.matchedFormats", "spec.profileHash"} {
		assert.Equal(t, string(rescan.FieldManager), managerFor(t, mf.ManagedFields, "", leaf), leaf)
	}
}

// A scanned lossy track freezes the tier its bitrate puts it on, read from
// the file by a probe, as the import worker freezes an imported one: a CBR
// 320 MP3 is Lidarr's MP3-320, on the High tier. Without the probe its
// quality was unknown.
func TestHandleFreezesAProbedLossyTrack(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH")
	}
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-probe", catalogv1alpha1.RootFolderKindMusic, "", catalogv1alpha1.ScanModeFull)
	createArtistAlbum(t, ctx, f)
	fixture, err := os.ReadFile("../../../testdata/mediainfo/audio_mp3_cbr320.mp3")
	require.NoError(t, err)
	track := filepath.Join(f.root, "Radiohead", "OK Computer (1997)", "02 - Paranoid Android.mp3")
	require.NoError(t, os.MkdirAll(filepath.Dir(track), 0o755))
	require.NoError(t, os.WriteFile(track, fixture, 0o644))

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))
	require.Equal(t, int64(1), readProgress(t, ctx, f.bus, string(f.scan.UID)).FilesMatched)
	mf := mediaFilesIn(t, ctx, f.c, f.ns, 1)[0]
	assert.Equal(t, "High", mf.Spec.Quality.Name)
}

// ReleaseTitle custom formats read a scanned file's name, as Radarr's
// MovieFile input does for a file with no recorded release: a REPACK HDR
// file scores repack-proper and hdr on top of its tier. Before X3's
// catalogue fix they read the parsed movie title and never matched; after
// it, a scan that passed no name still matched none.
func TestHandleScoresReleaseTitleFormatsFromTheFileName(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	qp := scoringProfile(t, ctx, c, "qp-rw-release-title")
	f := newFixture(t, ctx, "rw-release-title", catalogv1alpha1.RootFolderKindMovie, qp.Name, catalogv1alpha1.ScanModeFull)
	mustWriteFile(t, filepath.Join(f.root, "Heat (1995) [tmdbid-949]",
		"Heat.1995.REPACK.1080p.BluRay.HDR.x264-CtrlHD [tmdbid-949].mkv"), sampleFloor)

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))
	require.Equal(t, int64(1), readProgress(t, ctx, f.bus, string(f.scan.UID)).FilesMatched)

	resolved, errs := quality.FromCRD(qp, catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	mf := mediaFilesIn(t, ctx, f.c, f.ns, 1)[0]
	assert.Subset(t, mf.Spec.MatchedFormats, []string{"repack-proper", "hdr", "hd-bluray-tier-01"})
	sum := 0
	for _, slug := range mf.Spec.MatchedFormats {
		sum += resolved.Scores[slug]
	}
	assert.Equal(t, int32(sum), mf.Spec.FormatScore, "the frozen score is the matched formats' sum")
}
