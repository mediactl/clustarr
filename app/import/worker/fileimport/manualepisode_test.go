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
	"github.com/mediactl/clustarr/app/grab/lifecycle"
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

// TestAManualImportTakesAnUnparsableFileForTheNamedEpisode: a season pack
// whose one file's name no episode pattern reads is held for a person, and
// the hold's hint is download.clustarr.io/import with override and a
// target. Under that intent the file is the named episode's; before, it was
// held again with the same rejection, so the hint was a dead end (found by
// the smoke test on kind, 2026-10-07). Without the intent it is still held.
func TestAManualImportTakesAnUnparsableFileForTheNamedEpisode(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	library, downloads := filepath.Join(dir, "media", "tv"), filepath.Join(dir, "downloads")
	require.NoError(t, os.MkdirAll(library, 0o755))
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	qp := testQualityProfile("hd")
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "tv", Namespace: "media"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: library, Kind: catalogv1alpha1.RootFolderKindSeries},
	}
	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "got", Namespace: "media", UID: "got-uid"},
		Spec: catalogv1alpha1.SeriesSpec{TvdbID: 121361, QualityProfileRef: qp.Name, RootFolderRef: rf.Name,
			SeriesType: catalogv1alpha1.SeriesTypeStandard},
		Status: catalogv1alpha1.SeriesStatus{Metadata: &catalogv1alpha1.SeriesMetadata{
			Title: "Game of Thrones", Year: 2011, OriginalLanguage: "en",
		}},
	}
	episode := func(n int32) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: "got-s01e0" + string(rune('0'+n)), Namespace: "media"},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: series.Name, SeasonNumber: 1, EpisodeNumber: n},
		}
	}
	c, writes := readOnlyClient(t, qp, rf, series, episode(1), episode(2))

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
	// The pack covers both episodes (the downloads stage's v.Covered).
	covered := []schema.ItemRef{
		{Kind: "Episode", Ref: schema.Ref{Namespace: "media", Name: "got-s01e01"}},
		{Kind: "Episode", Ref: schema.Ref{Namespace: "media", Name: "got-s01e02"}},
	}
	run := func(uid string) *importRun {
		root := filepath.Join(downloads, uid)
		mustWriteSparseFile(t, filepath.Join(root, "Clustarr.Fixture.2010.1080p.BluRay.x264-CLUSTARR.REPACK.mkv"), sampleFloor)
		return &importRun{t: t, w: w, bus: bus, existing: c, in: importplan.Input{
			Owner: owner, OwnerKind: commonv1.MediaKindSeries,
			Entry: catalogv1alpha1.DownloadEntry{
				ID: "dl-" + uid, UID: uid,
				Episodes: []catalogv1alpha1.EpisodeNumber{{Season: 1, Number: 1}, {Season: 1, Number: 2}},
				Release: catalogv1alpha1.DownloadRelease{
					Title: "Game.of.Thrones.S01.1080p.WEB-DL.DD5.1.H.264-GRP", Protocol: commonv1.ProtocolUsenet,
				},
				GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: metav1.NewTime(now.Add(-time.Hour)),
				Phase: commonv1.DownloadPhaseCompleted,
			},
			Transfer: &schema.TransferRecord{ContentRoot: root}, Targets: covered,
			ProfileRef: qp.Name, Profile: &prof, OriginalLanguage: "en", Now: now,
		}}
	}

	// Without a person: held, needsPerson.
	auto := run("auto-entry")
	auto.decide()
	auto.deliver()
	insp := auto.record(schema.ImportSubInspect)
	require.NotNil(t, insp)
	require.NotNil(t, insp.Inspect)
	require.Len(t, insp.Inspect.Files, 1, "%+v", insp.Inspect)
	require.Len(t, insp.Inspect.Files[0].Rejections, 1, "%+v", insp.Inspect.Files[0])
	assert.Equal(t, string(commonv1.ImportClassNeedsPerson), insp.Inspect.Files[0].Rejections[0].Class)

	// A person names the episode: the file is that episode's.
	byHand := run("manual-entry")
	byHand.in.Intent = &lifecycle.ImportIntent{
		Nonce: "n1", ID: byHand.in.Entry.ID, Override: true,
		Target: &schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Name: "got-s01e01"}},
	}
	byHand.decide()
	byHand.in.Intent = nil
	byHand.deliver()
	insp = byHand.record(schema.ImportSubInspect)
	require.NotNil(t, insp)
	require.NotNil(t, insp.Inspect)
	require.Len(t, insp.Inspect.Files, 1)
	f := insp.Inspect.Files[0]
	assert.Empty(t, f.Rejections)
	require.NotNil(t, f.Proposed)
	assert.Equal(t, "got-s01e01", f.Proposed.Name)
	assert.Equal(t, []string{"got-s01e01"}, f.Keys)
	require.NotNil(t, f.Frozen.Quality)
	assert.Equal(t, "Bluray-1080p", f.Frozen.Quality.Name)
	assert.Contains(t, f.Dest, "S01E01")

	assert.Empty(t, writes(), "the import agent asked for Kubernetes writes")
}
