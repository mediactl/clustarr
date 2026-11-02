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

// An entry the manager re-created from its transfer's claim names no
// episode (a claim carries none), so the downloads stage gives its import
// the Series itself as the one target. Its files are attributed by their
// own names, as a pack's are: a lone target is the episode to import only
// when it is an Episode. Read as a key, the Series named an "episode"
// after itself, and every such import was held for a person and its files
// removed a day later (talos, 2026-11-02: At.Home.With.Amy.Sedaris.S03E09
// and In.Treatment S02E03, re-created on 2026-10-30).
func TestAnEntryNamingNoEpisodeImportsByItsFilesNames(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	library, downloads := filepath.Join(dir, "media", "tv"), filepath.Join(dir, "downloads")
	require.NoError(t, os.MkdirAll(library, 0o755))
	now := time.Date(2026, 11, 2, 20, 37, 0, 0, time.UTC)

	qp := testQualityProfile("hd")
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "tv", Namespace: "media"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: library, Kind: catalogv1alpha1.RootFolderKindSeries},
	}
	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "at-home-with-amy-sedaris", Namespace: "media", UID: "amy-uid"},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 335549, QualityProfileRef: qp.Name, RootFolderRef: rf.Name},
		Status: catalogv1alpha1.SeriesStatus{Metadata: &catalogv1alpha1.SeriesMetadata{
			Title: "At Home with Amy Sedaris", Year: 2017, OriginalLanguage: "en",
		}},
	}
	objs := []client.Object{qp, rf, series}
	for n := int32(1); n <= 10; n++ {
		objs = append(objs, &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-s03e%02d", series.Name, n), Namespace: "media"},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: series.Name, SeasonNumber: 3, EpisodeNumber: n},
		})
	}
	c, _ := readOnlyClient(t, objs...)

	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	prober := probetest.ProberFunc(func(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
		return &commonv1.MediaInfo{VideoCodec: "h264", Width: 1920, Height: 1080}, nil, nil
	})
	w := fileimport.NewWorker(c, bus, prober)
	w.Clock = func() time.Time { return now }
	prof, errs := quality.FromCRD(qp, catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	owner := schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "media", Name: series.Name, UID: string(series.UID)}}

	const release = "At.Home.With.Amy.Sedaris.S03E09.1080p.WEBRip.x264-KOMPOST"
	root := filepath.Join(downloads, "rebuilt")
	mustWriteSparseFile(t, filepath.Join(root, release+".mkv"), sampleFloor)
	run := &importRun{t: t, w: w, bus: bus, existing: c, in: importplan.Input{
		Owner: owner, OwnerKind: commonv1.MediaKindSeries,
		Entry: catalogv1alpha1.DownloadEntry{
			ID: "dl-rebuilt", UID: "rebuilt",
			Release:   catalogv1alpha1.DownloadRelease{Title: release, Protocol: commonv1.ProtocolUsenet},
			GrabbedBy: commonv1.GrabSourcePush, GrabbedAt: metav1.NewTime(now.Add(-72 * time.Hour)),
			Phase: commonv1.DownloadPhaseCompleted, Message: "re-created from its transfer's claim",
		},
		Transfer: &schema.TransferRecord{ContentRoot: root},
		// What the downloads stage gives an entry covering no item.
		Targets:    []schema.ItemRef{owner},
		ProfileRef: qp.Name, Profile: &prof, OriginalLanguage: "en", Now: now,
	}}
	run.decide()
	run.deliver()

	rec := run.record(schema.ImportSubInspect)
	require.NotNil(t, rec)
	require.NotNil(t, rec.Inspect)
	assert.Empty(t, rec.Inspect.Rejections, "the import is not held for a person: %+v", rec.Inspect.Rejections)
	require.Len(t, rec.Inspect.Files, 1, "%+v", rec.Inspect)
	f := rec.Inspect.Files[0]
	assert.Empty(t, f.Rejections, "%+v", f.Rejections)
	require.NotNil(t, f.Proposed)
	assert.Equal(t, series.Name+"-s03e09", f.Proposed.Name)
}
