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
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

// A season pack whose files no pattern reads imports all the same, each
// file as the episode its number or its title says in the download's
// season, at the quality and group the release title names (owner,
// 2026-11-27: "The import/rename should build the target name
// intelligently and not just verify that it can be perfectly parsed as
// is"). The names here are the held Gosick pack's with the series' title
// shortened past what titledEpisode knows, and one file named by its
// episode's title alone; the release title is that pack's.
func TestAPackNoPatternReadsImportsByItsSeasonsEpisodes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	library, root := filepath.Join(dir, "media", "anime"), filepath.Join(dir, "downloads", "gosick")
	require.NoError(t, os.MkdirAll(library, 0o755))
	now := time.Date(2026, 11, 27, 19, 0, 0, 0, time.UTC)

	qp := testQualityProfile("hd")
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "anime", Namespace: "media"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: library, Kind: catalogv1alpha1.RootFolderKindSeries},
	}
	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "gosick", Namespace: "media", UID: "gosick-uid"},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID: 219771, QualityProfileRef: qp.Name, RootFolderRef: rf.Name,
			SeriesType: catalogv1alpha1.SeriesTypeAnime,
		},
		Status: catalogv1alpha1.SeriesStatus{Metadata: &catalogv1alpha1.SeriesMetadata{
			Title: "Gosick", Year: 2011, OriginalLanguage: "ja",
		}},
	}
	titles := []string{
		"The Black Reaper Finds the Golden Fairy", "The Souls of the Dead Raise a Shipwreck",
		"The Souls of the Dead Raise the Sunken Ship", "The Golden Thread Shreds the Moment",
	}
	objs := []client.Object{qp, rf, series}
	var numbers []catalogv1alpha1.EpisodeNumber
	var targets []schema.ItemRef
	for i, title := range titles {
		n := int32(i + 1) //nolint:gosec // four episodes
		name := fmt.Sprintf("gosick-s01e%02d", n)
		objs = append(objs, &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: series.Name, SeasonNumber: 1, EpisodeNumber: n},
			Status:     catalogv1alpha1.EpisodeStatus{Title: title},
		})
		numbers = append(numbers, catalogv1alpha1.EpisodeNumber{Season: 1, Number: n})
		targets = append(targets, schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: "media", Name: name}})
	}
	c, writes := readOnlyClient(t, objs...)

	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	prober := probetest.ProberFunc(func(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
		return &commonv1.MediaInfo{VideoCodec: "hevc", VideoBitDepth: 10, Width: 1920, Height: 1080}, nil, nil
	})
	w := fileimport.NewWorker(c, bus, prober)
	w.Clock = func() time.Time { return now }
	prof, errs := quality.FromCRD(qp, catalogue.LoadedCatalogue())
	require.Empty(t, errs)

	files := map[string]string{
		"Gsk 01 The Dark Reaper Finds the Golden Fairy.mkv":           "gosick-s01e01",
		"Gsk 02 The Souls of the Dead Raise a Shipwreck.mkv":          "gosick-s01e02",
		"Gsk 03 The Hares Break Promises Beneath the Morning Sun.mkv": "gosick-s01e03",
		"Gsk 04 The Golden Thread Cuts Through a Passing Moment.mkv":  "gosick-s01e04",
	}
	for name := range files {
		mustWriteSparseFile(t, filepath.Join(root, name), sampleFloor)
	}
	run := &importRun{t: t, w: w, bus: bus, existing: c, in: importplan.Input{
		Owner:     schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "media", Name: series.Name, UID: string(series.UID)}},
		OwnerKind: commonv1.MediaKindSeries,
		Entry: catalogv1alpha1.DownloadEntry{
			ID: "dl-gosick", UID: "gosick-pack-uid", Episodes: numbers,
			Release: catalogv1alpha1.DownloadRelease{
				Title: "Gosick  [Dual Audio] [HEVC] [10-bit] [BDRip] [HD 1080p] [bluury]", Protocol: commonv1.ProtocolUsenet,
			},
			GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: metav1.NewTime(now.Add(-time.Hour)),
			Phase: commonv1.DownloadPhaseCompleted,
		},
		Transfer:   &schema.TransferRecord{ContentRoot: root},
		Targets:    targets,
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
			assert.Contains(t, filepath.Base(f.Dest), fmt.Sprintf("S01E%s", files[name][len(files[name])-2:]), "renamed for its episode: %s", f.Dest)
		})
	}
	assert.Empty(t, writes(), "the import agent asked for Kubernetes writes")
}
