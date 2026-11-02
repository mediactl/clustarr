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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

// The download's one video file is the episode its release title names
// when the file's own name names none of the series' episodes. GUACAMOLE's
// Drifters Blu-rays (talos, 2026-11-02) hold "gua-drifters.2016.e08-1080p.mkv",
// read as absolute episode 1080, and an engine once renamed a lone file to
// its entry id, whose hash read as absolute episode 50 (2026-10-27): each
// was held for a person while its release title said S01E08. Only a lone
// file takes the release's numbering, and a person's named episode takes a
// lone file whatever its name says.
func TestALoneFileTheReleaseTitleNamesIsThatEpisode(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	library, downloads := filepath.Join(dir, "media", "tv"), filepath.Join(dir, "downloads")
	require.NoError(t, os.MkdirAll(library, 0o755))
	now := time.Date(2026, 11, 2, 20, 34, 0, 0, time.UTC)

	qp := testQualityProfile("hd")
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "tv", Namespace: "media"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: library, Kind: catalogv1alpha1.RootFolderKindSeries},
	}
	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "drifters", Namespace: "media", UID: "drifters-uid"},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID: 315387, QualityProfileRef: qp.Name, RootFolderRef: rf.Name,
			SeriesType: catalogv1alpha1.SeriesTypeAnime,
		},
		Status: catalogv1alpha1.SeriesStatus{Metadata: &catalogv1alpha1.SeriesMetadata{
			Title: "Drifters", Year: 2016, OriginalLanguage: "ja",
		}},
	}
	objs := []client.Object{qp, rf, series}
	for n := int32(1); n <= 12; n++ {
		objs = append(objs, &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("drifters-s01e%02d", n), Namespace: "media"},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: series.Name, SeasonNumber: 1, EpisodeNumber: n},
			Status:     catalogv1alpha1.EpisodeStatus{AbsoluteNumber: ptr.To(n)},
		})
	}
	c, writes := readOnlyClient(t, objs...)

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

	const episode8 = "Drifters.Battle.in.a.Brand-New.World.War.S01E08.1080p.BluRay.x264-GUACAMOLE-Z0iDS3N"
	inspect := func(t *testing.T, uid, releaseTitle string, intent *lifecycle.ImportIntent, files ...string) []schema.InspectedFile {
		t.Helper()
		root := filepath.Join(downloads, uid)
		for _, f := range files {
			mustWriteSparseFile(t, filepath.Join(root, f), sampleFloor)
		}
		run := &importRun{t: t, w: w, bus: bus, existing: c, in: importplan.Input{
			Owner: owner, OwnerKind: commonv1.MediaKindSeries,
			Entry: catalogv1alpha1.DownloadEntry{
				ID: "dl-" + uid, UID: uid,
				Episodes:  []catalogv1alpha1.EpisodeNumber{{Season: 1, Number: 8}},
				Release:   catalogv1alpha1.DownloadRelease{Title: releaseTitle, Protocol: commonv1.ProtocolUsenet},
				GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: metav1.NewTime(now.Add(-time.Hour)),
				Phase: commonv1.DownloadPhaseCompleted,
			},
			Transfer:   &schema.TransferRecord{ContentRoot: root},
			Targets:    []schema.ItemRef{{Kind: "Episode", Ref: schema.Ref{Namespace: "media", Name: "drifters-s01e08"}}},
			ProfileRef: qp.Name, Profile: &prof, OriginalLanguage: "ja", Now: now,
		}}
		run.in.Intent = intent
		run.decide()
		run.in.Intent = nil
		run.deliver()
		rec := run.record(schema.ImportSubInspect)
		require.NotNil(t, rec)
		require.NotNil(t, rec.Inspect)
		require.Len(t, rec.Inspect.Files, len(files), "%+v", rec.Inspect)
		return rec.Inspect.Files
	}
	attributed := func(t *testing.T, f schema.InspectedFile, episode string) {
		t.Helper()
		assert.Empty(t, f.Rejections, "%+v", f.Rejections)
		require.NotNil(t, f.Proposed)
		assert.Equal(t, episode, f.Proposed.Name)
		assert.Equal(t, []string{episode}, f.Keys)
	}
	held := func(t *testing.T, f schema.InspectedFile) {
		t.Helper()
		require.Len(t, f.Rejections, 1, "%+v", f)
		assert.Equal(t, string(commonv1.ImportClassNeedsPerson), f.Rejections[0].Class)
		assert.Nil(t, f.Proposed)
	}
	// Each file's name, as the parser reads it.
	const (
		drifters = "gua-drifters.2016.e08-1080p.mkv"         // absolute 8
		entryID  = "86-eighty-six-cb83c58857-50e5c90a59.mkv" // absolute 50
		other    = "gua-drifters.2016.e09-1080p.mkv"         // absolute 9
		noSuch   = "Drifters.S01E50.1080p.BluRay.x264-GRP"   // S01E50
		target8  = "drifters-s01e08"
	)

	t.Run("the file's own name, read right", func(t *testing.T) {
		files := inspect(t, "own-name", episode8, nil, drifters)
		attributed(t, files[0], target8)
	})
	t.Run("a lone file its name cannot attribute takes the release title's episode", func(t *testing.T) {
		files := inspect(t, "lone", episode8, nil, entryID)
		attributed(t, files[0], target8)
		assert.Equal(t, []int32{1}, files[0].Parsed.Seasons, "the numbering recorded is the one the file was attributed by")
		assert.Equal(t, []int32{8}, files[0].Parsed.Episodes)
		assert.Empty(t, files[0].Parsed.Absolute)
	})
	t.Run("a file beside another video file keeps its own name's verdict", func(t *testing.T) {
		files := inspect(t, "two-files", episode8, nil, entryID, other)
		for _, f := range files {
			if filepath.Base(f.Path) == entryID {
				held(t, f)
			} else {
				attributed(t, f, "drifters-s01e09")
			}
		}
	})
	t.Run("a release title that names no episode either holds the lone file", func(t *testing.T) {
		files := inspect(t, "no-such", noSuch, nil, entryID)
		held(t, files[0])
	})
	t.Run("a person's named episode takes the lone file whatever its name says", func(t *testing.T) {
		intent := &lifecycle.ImportIntent{
			Nonce: "n1", ID: "dl-by-hand", Override: true,
			Target: &schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Name: target8}},
		}
		files := inspect(t, "by-hand", noSuch, intent, entryID)
		attributed(t, files[0], target8)
	})

	assert.Empty(t, writes(), "the import agent asked for Kubernetes writes")
}
