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

package scanapply

import (
	"context"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/scanprogress"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/names"
)

// TestTheApplierNeverAttachesAFileToAnItemOfAnotherRootFolder is smoke
// finding O4 at the manager, which decides every attribution (ADR-0019
// §7.6): a scanned file under one root folder attributed to -- or proposing
// -- the Movie that carries its TMDB id, stored under another root folder,
// is refused and listed unmatched, naming that Movie and its root folder;
// no MediaFile is recorded outside its item's root folder and the Movie is
// left as it is. A Series proposed by a folder whose TheTVDB id is another
// root folder's is refused the same way.
func TestTheApplierNeverAttachesAFileToAnItemOfAnotherRootFolder(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)
	root := func(name string, kind catalogv1alpha1.RootFolderKind) *catalogv1alpha1.RootFolder {
		return &catalogv1alpha1.RootFolder{
			ObjectMeta: metav1.ObjectMeta{Namespace: "films", Name: name},
			Spec: catalogv1alpha1.RootFolderSpec{
				Path: "/data/media/" + name, Kind: kind,
				Defaults: catalogv1alpha1.RootDefaults{QualityProfileRef: "hd"},
			},
		}
	}
	hd, uhd := root("movies", catalogv1alpha1.RootFolderKindMovie), root("movies-4k", catalogv1alpha1.RootFolderKindMovie)
	shows, anime := root("shows", catalogv1alpha1.RootFolderKindSeries), root("anime", catalogv1alpha1.RootFolderKindSeries)
	inception := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Namespace: "films", Name: names.Movie("Inception", 27205)},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 27205, QualityProfileRef: "hd", RootFolderRef: hd.Name},
	}
	andor := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Namespace: "films", Name: names.Series("Andor", 393189)},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 393189, QualityProfileRef: "hd", RootFolderRef: shows.Name},
	}
	movie := string(commonv1.MediaKindMovie)
	cases := []struct {
		name, within, owner, item string
		path                      string
		attribution               *schema.ScanAttribution
	}{{
		name: "an attribution to the movie", within: uhd.Name, owner: hd.Name, item: inception.Name,
		path:        "/data/media/movies-4k/Inception (2010) {tmdb-27205}/Inception (2010).mkv",
		attribution: &schema.ScanAttribution{Kind: movie, Name: inception.Name, ProviderID: "tmdb:27205", Evidence: "tmdb:27205"},
	}, {
		name: "a proposal of the movie", within: uhd.Name, owner: hd.Name, item: inception.Name,
		path: "/data/media/movies-4k/Inception (2010) {tmdb-27205}/Inception (2010).mkv",
		attribution: &schema.ScanAttribution{Kind: movie, Name: inception.Name, ProviderID: "tmdb:27205", Evidence: "tmdb:27205",
			Create: &schema.ItemCreate{Kind: movie, Name: inception.Name, TmdbID: 27205, Title: "Inception", Year: 2010,
				QualityProfileRef: "hd", RootFolderRef: uhd.Name}},
	}, {
		name: "a proposal of the series", within: anime.Name, owner: shows.Name, item: andor.Name,
		path: "/data/media/anime/Andor (2022) {tvdb-393189}/Season 01/Andor - S01E01.mkv",
		attribution: &schema.ScanAttribution{Kind: string(commonv1.MediaKindSeries), Name: andor.Name, ProviderID: "tvdb:393189", Evidence: "folder",
			Create: &schema.ItemCreate{Kind: string(commonv1.MediaKindSeries), Name: andor.Name, TvdbID: 393189, Title: "Andor", Year: 2022,
				QualityProfileRef: "hd", RootFolderRef: anime.Name}},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus := membus.New(clockwork.NewRealClock())
			require.NoError(t, bus.Ensure(ctx, events.Default()))
			t.Cleanup(func() { _ = bus.Close() })
			c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
				WithObjects(hd, uhd, shows, anime, inception.DeepCopy(), andor.DeepCopy()).Build()
			a := &Applier{Client: c, Reader: c, APIReader: c, Bus: bus, Now: func() time.Time { return now }}
			scan := schema.Ref{Namespace: "films", Name: "scan", UID: "scan-uid"}
			require.NoError(t, a.Apply(ctx, schema.ScanObservation{
				Scan: scan, RootFolder: schema.Ref{Namespace: "films", Name: tc.within},
				Kind: schema.ScanObservationPresent, Path: tc.path, SizeBytes: 1, ModTime: now,
				Attribution: tc.attribution, Frozen: &schema.FrozenFields{}, SeenAt: now,
			}))

			var files catalogv1alpha1.MediaFileList
			require.NoError(t, c.List(ctx, &files))
			assert.Empty(t, files.Items, "a file under %s was recorded against an item of %s", tc.within, tc.owner)
			var stored catalogv1alpha1.Movie
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(inception), &stored))
			assert.Equal(t, hd.Name, stored.Spec.RootFolderRef)

			entry, err := bus.KV(events.BucketProgress).Get(ctx, scanprogress.AppliedKey(scan.UID))
			require.NoError(t, err, "the applier recorded no tally")
			p, err := scanprogress.DecodeProgress(entry.Value)
			require.NoError(t, err)
			require.Len(t, p.Unmatched, 1)
			assert.Contains(t, p.Unmatched[0].Reason, `root folder "`+tc.owner+`"`)
			assert.Equal(t, []string{tc.item}, p.Unmatched[0].Candidates)
			assert.Zero(t, p.FilesMatched)
		})
	}
}
