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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestASeasonPackNamedForItsEpisodesAloneImports: a season pack whose files
// name only their episode is attributed by that numbering, at the quality,
// group and languages the release title names. A Sign of Affection's EMBER
// pack (talos, 2026-10-27) named its files "S01E01-Yuki's.World.[...].mkv",
// left by the engine inside its NZB subjects' wrapper; every file was held
// as unparseable, and a name with no quality would have read Unknown, which
// no profile allows.
func TestASeasonPackNamedForItsEpisodesAloneImports(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	library, root := filepath.Join(dir, "media", "tv"), filepath.Join(dir, "downloads", "pack")
	require.NoError(t, os.MkdirAll(library, 0o755))
	now := time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC)

	qp := testQualityProfile("hd")
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "tv", Namespace: "media"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: library, Kind: catalogv1alpha1.RootFolderKindSeries},
	}
	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "aso", Namespace: "media", UID: "aso-uid"},
		Spec: catalogv1alpha1.SeriesSpec{TvdbID: 425458, QualityProfileRef: qp.Name, RootFolderRef: rf.Name,
			SeriesType: catalogv1alpha1.SeriesTypeAnime},
		Status: catalogv1alpha1.SeriesStatus{Metadata: &catalogv1alpha1.SeriesMetadata{
			Title: "A Sign of Affection", Year: 2024, OriginalLanguage: "ja",
		}},
	}
	episode := func(n int32) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: "aso-s01e0" + string(rune('0'+n)), Namespace: "media"},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: series.Name, SeasonNumber: 1, EpisodeNumber: n},
		}
	}
	c, writes := readOnlyClient(t, qp, rf, series, episode(1), episode(2))

	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	prober := probetest.ProberFunc(func(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
		return &commonv1.MediaInfo{VideoCodec: "hevc", Width: 1920, Height: 1080}, nil, nil
	})
	w := fileimport.NewWorker(c, bus, prober)
	w.Clock = func() time.Time { return now }
	prof, errs := quality.FromCRD(qp, catalogue.LoadedCatalogue())
	require.Empty(t, errs)

	files := map[string]string{
		"[PRiVATE]-[EnCrYpTnZb]-[S01E01-Yuki's.World.[D7A77AE7].mkv].mkv": "aso-s01e01",
		"S01E02 - To Affection.mkv":                                       "aso-s01e02",
	}
	for name := range files {
		mustWriteSparseFile(t, filepath.Join(root, name), sampleFloor)
	}
	run := &importRun{t: t, w: w, bus: bus, existing: c, in: importplan.Input{
		Owner:     schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "media", Name: series.Name, UID: string(series.UID)}},
		OwnerKind: commonv1.MediaKindSeries,
		Entry: catalogv1alpha1.DownloadEntry{
			ID: "dl-pack", UID: "pack-uid",
			Episodes: []catalogv1alpha1.EpisodeNumber{{Season: 1, Number: 1}, {Season: 1, Number: 2}},
			Release: catalogv1alpha1.DownloadRelease{
				Title: "A.Sign.of.Affection.S01.1080p.Dual.Audio.BDRip.10.bits.DD.x265-EMBER-xpost", Protocol: commonv1.ProtocolUsenet,
			},
			GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: metav1.NewTime(now.Add(-time.Hour)),
			Phase: commonv1.DownloadPhaseCompleted,
		},
		Transfer: &schema.TransferRecord{ContentRoot: root},
		Targets: []schema.ItemRef{
			{Kind: "Episode", Ref: schema.Ref{Namespace: "media", Name: "aso-s01e01"}},
			{Kind: "Episode", Ref: schema.Ref{Namespace: "media", Name: "aso-s01e02"}},
		},
		ProfileRef: qp.Name, Profile: &prof, OriginalLanguage: "ja", Now: now,
	}}
	run.decide()
	run.deliver()
	insp := run.record(schema.ImportSubInspect)
	require.NotNil(t, insp)
	require.NotNil(t, insp.Inspect)
	require.Len(t, insp.Inspect.Files, len(files), "%+v", insp.Inspect)
	for _, f := range insp.Inspect.Files {
		name := filepath.Base(f.Path)
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, f.Rejections)
			require.NotNil(t, f.Proposed)
			assert.Equal(t, files[name], f.Proposed.Name)
			require.NotNil(t, f.Frozen.Quality)
			assert.Equal(t, "Bluray-1080p", f.Frozen.Quality.Name, "the release title's quality")
			assert.Equal(t, "EMBER", f.Frozen.ReleaseGroup, "the release's group, not one read from the episode's name")
			assert.Contains(t, f.Frozen.Languages, "Japanese")
			assert.Contains(t, f.Frozen.Languages, "English", "the release title's dual audio")
		})
	}
	assert.Empty(t, writes(), "the import agent asked for Kubernetes writes")
}
