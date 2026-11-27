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
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/import/importplan"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/app/import/worker/probe/probetest"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/metadata/scenemap"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// sceneMaps is a literal TheXEM, by TVDB id.
type sceneMaps map[int64]*scenemap.Map

func (m sceneMaps) SceneMap(_ context.Context, tvdbID int64) (*scenemap.Map, error) {
	return m[tvdbID], nil
}

// seasonNames is TheXEM's names table for a series whose scene counts each
// of its two twelve-episode seasons from 1, as it does for most anime.
func seasonNames(names map[string]int) *scenemap.Map {
	m := &scenemap.Map{TVDBID: 1}
	for s := 1; s <= 2; s++ {
		for n := 1; n <= 12; n++ {
			m.Mappings = append(m.Mappings, scenemap.Mapping{
				Scene: scenemap.Numbering{Season: s, Episode: n, Absolute: n},
				TVDB:  scenemap.Numbering{Season: s, Episode: n, Absolute: (s-1)*12 + n},
			})
		}
	}
	for title, season := range names {
		m.Names = append(m.Names, scenemap.SceneName{Title: title, Season: &season})
	}
	return m
}

// inspectedAs runs the import of one download of "Show", an anime of two
// twelve-episode seasons TVDB counts through (season 2's first episode is
// its thirteenth), from the downloads stage's input to the agent's
// inspection, and answers the episode each file was attributed to, "" for a
// file held. grabbed are the episodes the grab took the release for.
func inspectedAs(t *testing.T, title string, grabbed []catalogv1alpha1.EpisodeNumber, scene *scenemap.Map, files ...string) map[string]string {
	t.Helper()
	return inspectedByHandAs(t, "", title, grabbed, scene, files...)
}

// inspectedByHandAs is inspectedAs for an import a person runs, naming the
// episode named; "" for one nobody runs.
func inspectedByHandAs(
	t *testing.T, named, title string, grabbed []catalogv1alpha1.EpisodeNumber, scene *scenemap.Map, files ...string,
) map[string]string {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	library, root := filepath.Join(dir, "media", "anime"), filepath.Join(dir, "downloads", "show")
	require.NoError(t, os.MkdirAll(library, 0o755))
	now := time.Date(2026, 11, 27, 18, 0, 0, 0, time.UTC)

	qp := testQualityProfile("hd")
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "anime", Namespace: "media"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: library, Kind: catalogv1alpha1.RootFolderKindSeries},
	}
	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "show", Namespace: "media", UID: "show-uid"},
		Spec: catalogv1alpha1.SeriesSpec{TvdbID: 1, QualityProfileRef: qp.Name, RootFolderRef: rf.Name,
			SeriesType: catalogv1alpha1.SeriesTypeAnime},
		Status: catalogv1alpha1.SeriesStatus{Metadata: &catalogv1alpha1.SeriesMetadata{
			Title: "Show", Year: 2024, OriginalLanguage: "ja",
		}},
	}
	objs := []client.Object{qp, rf, series}
	for s := int32(1); s <= 2; s++ {
		for n := int32(1); n <= 12; n++ {
			abs := (s-1)*12 + n
			ep := &catalogv1alpha1.Episode{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("show-s%02de%02d", s, n), Namespace: "media"},
				Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: series.Name, SeasonNumber: s, EpisodeNumber: n},
			}
			ep.Status.AbsoluteNumber = &abs
			objs = append(objs, ep)
		}
	}
	c, _ := readOnlyClient(t, objs...)

	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	prober := probetest.ProberFunc(func(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
		return &commonv1.MediaInfo{VideoCodec: "hevc", Width: 1920, Height: 1080}, nil, nil
	})
	w := fileimport.NewWorker(c, bus, prober)
	w.Clock = func() time.Time { return now }
	if scene != nil {
		w.SceneMaps = sceneMaps{1: scene}
	}
	prof, errs := quality.FromCRD(qp, catalogue.LoadedCatalogue())
	require.Empty(t, errs)

	for _, name := range files {
		mustWriteSparseFile(t, filepath.Join(root, name), sampleFloor)
	}
	var targets []schema.ItemRef
	for _, g := range grabbed {
		targets = append(targets, schema.ItemRef{Kind: "Episode", Ref: schema.Ref{
			Namespace: "media", Name: fmt.Sprintf("show-s%02de%02d", g.Season, g.Number),
		}})
	}
	run := &importRun{t: t, w: w, bus: bus, existing: c, in: importplan.Input{
		Owner:     schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "media", Name: series.Name, UID: string(series.UID)}},
		OwnerKind: commonv1.MediaKindSeries,
		Entry: catalogv1alpha1.DownloadEntry{
			ID: "dl-show", UID: "show-dl-uid", Episodes: grabbed,
			Release:   catalogv1alpha1.DownloadRelease{Title: title, Protocol: commonv1.ProtocolUsenet},
			GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: metav1.NewTime(now.Add(-time.Hour)),
			Phase: commonv1.DownloadPhaseCompleted,
		},
		Transfer:   &schema.TransferRecord{ContentRoot: root},
		Targets:    targets,
		ProfileRef: qp.Name, Profile: &prof, OriginalLanguage: "ja", Now: now,
	}}
	if named != "" {
		run.in.Intent = &lifecycle.ImportIntent{
			Nonce: "n1", ID: run.in.Entry.ID, Override: true,
			Target: &schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Name: named}},
		}
	}
	run.decide()
	run.in.Intent = nil
	run.deliver()
	insp := run.record(schema.ImportSubInspect)
	require.NotNil(t, insp)
	require.NotNil(t, insp.Inspect)
	require.Len(t, insp.Inspect.Files, len(files), "%+v", insp.Inspect)
	out := map[string]string{}
	for _, f := range insp.Inspect.Files {
		name := filepath.Base(f.Path)
		out[name] = ""
		if f.Proposed != nil {
			out[name] = f.Proposed.Name
			assert.Empty(t, f.Rejections, name)
		}
	}
	return out
}

func episodes(season int32, numbers ...int32) []catalogv1alpha1.EpisodeNumber {
	out := make([]catalogv1alpha1.EpisodeNumber, 0, len(numbers))
	for _, n := range numbers {
		out = append(out, catalogv1alpha1.EpisodeNumber{Season: season, Number: n})
	}
	return out
}

// A file named by a number and no season is an episode of the season its
// download is of: the download says the season, the name only the episode.
// Counted through the series whatever the download was, a season 2 pack
// whose files restart at 01 was filed as season 1 (talos: the twelve files
// of Ranma ½ (2024)'s season 2 pack on 2026-10-28, season 2's by their
// running times and season 1's in the library; found 2026-11-27).
func TestAFilesBareNumberIsReadInTheSeasonItsDownloadIsOf(t *testing.T) {
	const pack2 = "Show.S02.1080p.BluRay.Dual-Audio.FLAC.x265-GRP"
	for _, tc := range []struct {
		name    string
		title   string
		grabbed []catalogv1alpha1.EpisodeNumber
		scene   *scenemap.Map
		want    map[string]string
	}{
		{
			name:  "a season pack whose files restart at 01",
			title: pack2, grabbed: episodes(2, 1, 2),
			want: map[string]string{
				"[GRP] Show - 01 (BD 1080p) [AAAA1111].mkv": "show-s02e01",
				"[GRP] Show - 02 (BD 1080p) [BBBB2222].mkv": "show-s02e02",
			},
		},
		{
			name:  "a season pack whose files count on through the series",
			title: pack2, grabbed: episodes(2, 1, 2),
			want: map[string]string{
				"[GRP] Show - 13 (BD 1080p) [AAAA1111].mkv": "show-s02e01",
				"[GRP] Show - 14 (BD 1080p) [BBBB2222].mkv": "show-s02e02",
			},
		},
		{
			// The grab read the title through the series' scene names; this
			// import has none to read it through ("[Cleo]Date A Live II -
			// 07", filed as S01E07 on talos, 2026-11-05).
			name:  "an episode grabbed for a later season under a title the import cannot read",
			title: "[GRP] Show II - 03 (BD 1080p x265)", grabbed: episodes(2, 3),
			want: map[string]string{"[GRP] Show II - 03 (BD 1080p x265).mkv": "show-s02e03"},
		},
		{
			name:  "a release title that is a later season's scene name",
			title: "[GRP] Show Zwei - 03 (BD 1080p x265)", grabbed: episodes(2, 3),
			scene: seasonNames(map[string]int{"Show Zwei": 2}),
			want:  map[string]string{"[GRP] Show - 03 (BD 1080p x265).mkv": "show-s02e03"},
		},
		{
			// The name, not the download, says what such a file is.
			name:  "a name that says its season",
			title: pack2, grabbed: episodes(2, 4, 5),
			want: map[string]string{
				"Show.S01E05.1080p.BluRay.x265-GRP.mkv": "show-s01e05",
				"[GRP] Show S2 - 04 (BD 1080p).mkv":     "show-s02e04",
			},
		},
		{
			// A pack titled for one season that holds the cour TVDB lists
			// as the next: the season has no thirteenth episode, so the
			// count through the series stands.
			name:  "a count the season has no episode for",
			title: "Show.S01.1080p.BluRay.Dual-Audio.FLAC.x265-GRP", grabbed: episodes(1, 12),
			want: map[string]string{
				"[GRP] Show - 12 (BD 1080p) [AAAA1111].mkv": "show-s01e12",
				"[GRP] Show - 13 (BD 1080p) [BBBB2222].mkv": "show-s02e01",
			},
		},
		{
			// Of two seasons, a download says no season: the count.
			name:  "a pack of two seasons",
			title: "Show.S01-S02.1080p.BluRay.Dual-Audio.FLAC.x265-GRP", grabbed: episodes(2, 1),
			want: map[string]string{"[GRP] Show - 01 (BD 1080p) [AAAA1111].mkv": "show-s01e01"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var files []string
			for f := range tc.want {
				files = append(files, f)
			}
			assert.Equal(t, tc.want, inspectedAs(t, tc.title, tc.grabbed, tc.scene, files...))
		})
	}
}

// The episode a person names for a download is that person's to say: its
// release title's season is not read into the file's number over it.
func TestAPersonsNamedEpisodeIsNotReadInTheDownloadsSeason(t *testing.T) {
	const title, file = "[GRP] Show S2 - 03 (BD 1080p x265)", "[GRP] Show - 03 (BD 1080p x265).mkv"
	assert.Equal(t, map[string]string{file: "show-s02e03"}, inspectedAs(t, title, episodes(2, 3), nil, file),
		"nobody ran it: the release title's season")
	assert.Equal(t, map[string]string{file: "show-s01e03"}, inspectedByHandAs(t, "show-s01e03", title, episodes(2, 3), nil, file),
		"a person named season 1's third episode")
}

// Two seasons' scene names that differ in their punctuation alone are told
// apart by it: TheXEM's "New Game!" is season 1 and "New Game!!" season 2,
// and under one key every season 1 release was season 2's ("[DB]New Game! -
// 09" read as S02E09; found replaying talos' files, 2026-11-27).
func TestSceneNamesThatDifferInPunctuationAloneNameTheirOwnSeasons(t *testing.T) {
	scene := seasonNames(map[string]int{"Show!": 1, "Show!!": 2, "Show! S2": 2})
	for _, tc := range []struct {
		title   string
		grabbed []catalogv1alpha1.EpisodeNumber
		want    string
	}{
		{"[GRP]Show! - 09 (Dual Audio 10bit BD1080p x265)", episodes(1, 9), "show-s01e09"},
		{"[GRP]Show!! - 09 (Dual Audio 10bit BD1080p x265)", episodes(2, 9), "show-s02e09"},
	} {
		t.Run(tc.title, func(t *testing.T) {
			file := tc.title + ".mkv"
			assert.Equal(t, map[string]string{file: tc.want}, inspectedAs(t, tc.title, tc.grabbed, scene, file))
		})
	}
}
