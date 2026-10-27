/*
Copyright 2026 The clustarr Authors.

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
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/vansante/go-ffprobe.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importplan"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/app/import/worker/probe/probetest"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// An obfuscated season pack whose archive carried no par2 set for its
// files unpacks to hashed names, "02fb98288e35eea0f5b10ea420980959.mkv",
// which no usenet client can restore: the job's name is the season's, not
// any episode's. Uploaders who hash the file names often leave the
// container's title tag as it was, "Show.S01E01.1080p...", so the import
// reads each file's title for the numbering its name lacks.
func hashedPackRun(t *testing.T, titles map[string]string) (*importRun, func() map[string]int) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	library, root := filepath.Join(dir, "media", "tv"), filepath.Join(dir, "downloads", "pack")
	require.NoError(t, os.MkdirAll(library, 0o755))
	now := time.Date(2026, 10, 27, 7, 0, 0, 0, time.UTC)

	qp := testQualityProfile("hd")
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "tv", Namespace: "media"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: library, Kind: catalogv1alpha1.RootFolderKindSeries},
	}
	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "hgm", Namespace: "media", UID: "hgm-uid"},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 384783, QualityProfileRef: qp.Name, RootFolderRef: rf.Name},
		Status: catalogv1alpha1.SeriesStatus{Metadata: &catalogv1alpha1.SeriesMetadata{
			Title: "History's Greatest Mysteries", Year: 2020, OriginalLanguage: "en",
		}},
	}
	episode := func(n int32) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("hgm-s01e%02d", n), Namespace: "media"},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: series.Name, SeasonNumber: 1, EpisodeNumber: n},
		}
	}
	c, _ := readOnlyClient(t, qp, rf, series, episode(1), episode(2))

	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	var mu sync.Mutex
	probes := map[string]int{}
	prober := probetest.ProberFunc(func(_ context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
		mu.Lock()
		probes[filepath.Base(path)]++
		mu.Unlock()
		raw := &mediainfo.Raw{Format: &ffprobe.Format{TagList: ffprobe.Tags{}}}
		if title := titles[filepath.Base(path)]; title != "" {
			raw.Format.TagList["TITLE"] = title // Matroska keeps a tag's key as written
		}
		return &commonv1.MediaInfo{VideoCodec: "h264", Width: 1920, Height: 1080}, raw, nil
	})
	w := fileimport.NewWorker(c, bus, prober)
	w.Clock = func() time.Time { return now }
	prof, errs := quality.FromCRD(qp, catalogue.LoadedCatalogue())
	require.Empty(t, errs)

	for name := range titles {
		mustWriteSparseFile(t, filepath.Join(root, name), sampleFloor)
	}
	run := &importRun{t: t, w: w, bus: bus, existing: c, in: importplan.Input{
		Owner:     schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "media", Name: series.Name, UID: string(series.UID)}},
		OwnerKind: commonv1.MediaKindSeries,
		Entry: catalogv1alpha1.DownloadEntry{
			ID: "dl-pack", UID: "pack-uid",
			Episodes: []catalogv1alpha1.EpisodeNumber{{Season: 1, Number: 1}, {Season: 1, Number: 2}},
			Release: catalogv1alpha1.DownloadRelease{
				Title: "Historys.Greatest.Mysteries.S01.1080p.WEB-DL.DDP2.0.H.264-NTb", Protocol: commonv1.ProtocolUsenet,
			},
			GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: metav1.NewTime(now.Add(-time.Hour)),
			Phase: commonv1.DownloadPhaseCompleted,
		},
		Transfer: &schema.TransferRecord{ContentRoot: root},
		Targets: []schema.ItemRef{
			{Kind: "Episode", Ref: schema.Ref{Namespace: "media", Name: "hgm-s01e01"}},
			{Kind: "Episode", Ref: schema.Ref{Namespace: "media", Name: "hgm-s01e02"}},
		},
		ProfileRef: qp.Name, Profile: &prof, OriginalLanguage: "en", Now: now,
	}}
	return run, func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]int{}
		for k, v := range probes {
			out[k] = v
		}
		return out
	}
}

func TestAHashedPackFileIsNamedByItsContainerTitle(t *testing.T) {
	titles := map[string]string{
		"02fb98288e35eea0f5b10ea420980959.mkv": "Historys.Greatest.Mysteries.S01E01.1080p.WEB-DL.DDP2.0.H.264-NTb",
		"9c0a7f5b1d2e3a4b5c6d7e8f90123456.mkv": "Historys.Greatest.Mysteries.S01E02.1080p.WEB-DL.DDP2.0.H.264-NTb",
	}
	want := map[string]string{
		"02fb98288e35eea0f5b10ea420980959.mkv": "hgm-s01e01",
		"9c0a7f5b1d2e3a4b5c6d7e8f90123456.mkv": "hgm-s01e02",
	}
	run, probes := hashedPackRun(t, titles)
	run.decide()
	run.deliver()
	insp := run.record(schema.ImportSubInspect)
	require.NotNil(t, insp)
	require.NotNil(t, insp.Inspect)
	require.Len(t, insp.Inspect.Files, len(titles), "%+v", insp.Inspect)
	for _, f := range insp.Inspect.Files {
		name := filepath.Base(f.Path)
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, f.Rejections)
			require.NotNil(t, f.Proposed)
			assert.Equal(t, want[name], f.Proposed.Name)
			require.NotNil(t, f.Frozen.Quality)
			assert.Equal(t, "WEBDL-1080p", f.Frozen.Quality.Name)
			assert.Equal(t, "NTb", f.Frozen.ReleaseGroup)
		})
	}
	for name, n := range probes() {
		assert.Equal(t, 1, n, "%s was probed %d times; the title's probe is the import's", name, n)
	}
}

func TestAHashedPackFileWhoseTitleNamesNoEpisodeNeedsAPerson(t *testing.T) {
	titles := map[string]string{
		"02fb98288e35eea0f5b10ea420980959.mkv": "History's Greatest Mysteries",
	}
	run, _ := hashedPackRun(t, titles)
	run.decide()
	run.deliver()
	insp := run.record(schema.ImportSubInspect)
	require.NotNil(t, insp)
	require.NotNil(t, insp.Inspect)
	require.Len(t, insp.Inspect.Files, 1, "%+v", insp.Inspect)
	f := insp.Inspect.Files[0]
	require.Len(t, f.Rejections, 1, "%+v", f)
	assert.Nil(t, f.Proposed)
	assert.Contains(t, f.Rejections[0].Text, "could not parse the filename")
	assert.Contains(t, f.Rejections[0].Text, "container title", "the rejection says the title was read and named no episode")
}
