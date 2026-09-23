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

package rssmatcher

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/release"
)

// TestTitleYearKey pins the equality form: CleanTitle, not Normalize.
// Normalize's own doc comment says it is "not for equality comparison", and
// this key exists only to be compared -- a scene release writes "The.Matrix"
// where the provider wrote "The Matrix", and the two must collapse.
func TestTitleYearKey(t *testing.T) {
	want := TitleYearKey("The Matrix", 1999)
	assert.NotEmpty(t, want)
	for _, variant := range []string{"THE MATRIX", "the matrix", "Matrix, The", "The Matrix!"} {
		assert.Equalf(t, want, TitleYearKey(variant, 1999), "%q must collapse onto the same key", variant)
	}
	assert.NotEqual(t, want, TitleYearKey("The Matrix", 2003), "the year is part of the key")
	assert.Empty(t, TitleYearKey("", 1999), "an empty title indexes nothing")
	assert.Equal(t, release.CleanTitle("The Matrix")+"|1999", want)
}

func TestMovieTitleYearKeys(t *testing.T) {
	cases := []struct {
		name string
		obj  client.Object
		want []string
	}{
		{
			name: "no metadata yet indexes nothing: the item is still id-matchable",
			obj:  &catalogv1alpha1.Movie{},
			want: nil,
		},
		{
			name: "title only",
			obj: &catalogv1alpha1.Movie{Status: catalogv1alpha1.MovieStatus{
				Metadata: &catalogv1alpha1.MovieMetadata{Title: "The Thing", Year: 1982},
			}},
			want: []string{TitleYearKey("The Thing", 1982)},
		},
		{
			name: "a distinct original title gets its own key: an indexer may use either",
			obj: &catalogv1alpha1.Movie{Status: catalogv1alpha1.MovieStatus{
				Metadata: &catalogv1alpha1.MovieMetadata{Title: "Spirited Away", OriginalTitle: "Sen to Chihiro no Kamikakushi", Year: 2001},
			}},
			want: []string{
				TitleYearKey("Spirited Away", 2001),
				TitleYearKey("Sen to Chihiro no Kamikakushi", 2001),
			},
		},
		{
			name: "an identical original title is not indexed twice",
			obj: &catalogv1alpha1.Movie{Status: catalogv1alpha1.MovieStatus{
				Metadata: &catalogv1alpha1.MovieMetadata{Title: "The Thing", OriginalTitle: "The Thing", Year: 1982},
			}},
			want: []string{TitleYearKey("The Thing", 1982)},
		},
		{
			name: "a non-Movie object indexes nothing",
			obj:  &catalogv1alpha1.Series{},
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, movieTitleYearKeys(c.obj))
		})
	}
}

func TestSeriesTitleYearKeys(t *testing.T) {
	assert.Nil(t, seriesTitleYearKeys(&catalogv1alpha1.Series{}))
	assert.Equal(t, []string{"wire", "wire 2002"},
		seriesTitleYearKeys(&catalogv1alpha1.Series{Status: catalogv1alpha1.SeriesStatus{
			Metadata: &catalogv1alpha1.SeriesMetadata{Title: "The Wire", Year: 2002},
		}}), "a series answers to its title alone and to its title with its first-aired year")
	assert.Equal(t, []string{"doctor who 2005", "doctor who 2005 2005"},
		seriesTitleYearKeys(&catalogv1alpha1.Series{Status: catalogv1alpha1.SeriesStatus{
			Metadata: &catalogv1alpha1.SeriesMetadata{Title: "Doctor Who (2005)", Year: 2005},
		}}), "a title that already carries its year answers to that form")
}

// TestSeriesTitleKeyMatchesWhatTheParserProduces pins the lookup against
// what pkg/release really yields for a TV release: the year, when a release
// names one, stays INSIDE the parsed series title and Year is 0. Keying a
// series by "<title>|<year>" is what made every yearless release
// unmatchable by title.
func TestSeriesTitleKeyMatchesWhatTheParserProduces(t *testing.T) {
	for _, c := range []struct {
		release string
		want    string
	}{
		{"The.Wire.S01E02.720p.HDTV.x264-GRP", SeriesTitleKey("The Wire", 0)},
		{"Doctor.Who.2005.S01E01.720p.HDTV.x264-GRP", SeriesTitleKey("Doctor Who", 2005)},
		{"Doctor.Who.S01E01.720p.HDTV.x264-GRP", SeriesTitleKey("Doctor Who", 0)},
	} {
		p, err := release.Parse(c.release, release.Options{Kind: release.ClassifyKind(c.release)})
		if assert.NoError(t, err, c.release) {
			assert.Equal(t, c.want, SeriesTitleKey(p.Title, int32(p.Year)), c.release)
		}
	}
}

func TestSeasonKey(t *testing.T) {
	assert.Equal(t, "the-wire/1", seasonKey("the-wire", 1))
	assert.Equal(t, "the-wire/0", seasonKey("the-wire", 0), "season 0 is the specials season, not an absent one")
}

func TestPackRef(t *testing.T) {
	_, ok := packRef("the-wire", nil)
	assert.False(t, ok, "no episodes is no match")

	ref, ok := packRef("the-wire", []string{"the-wire-s01e01"})
	assert.True(t, ok)
	assert.Equal(t, "episode", string(ref.Kind), "a single episode is its own target, not a one-item pack")
	assert.Equal(t, "the-wire-s01e01", ref.Name)
	assert.Empty(t, ref.Keys)

	ref, ok = packRef("the-wire", []string{"the-wire-s01e01", "the-wire-s01e02"})
	assert.True(t, ok)
	assert.Equal(t, "series", string(ref.Kind), "a pack targets the Series and narrows with Keys")
	assert.Equal(t, "the-wire", ref.Name)
	assert.Equal(t, []string{"the-wire-s01e01", "the-wire-s01e02"}, ref.Keys)
}

func TestSameDay(t *testing.T) {
	// An indexer and a metadata provider rarely agree on the time of day, so
	// a daily episode must match on the calendar date alone.
	a := time.Date(2026, 9, 18, 2, 0, 0, 0, time.UTC)
	b := time.Date(2026, 9, 18, 23, 30, 0, 0, time.UTC)
	assert.True(t, sameDay(a, b))
	assert.False(t, sameDay(a, a.Add(24*time.Hour)))

	// And across zones, compared in UTC.
	east := time.FixedZone("UTC+2", 2*60*60)
	assert.True(t, sameDay(a, time.Date(2026, 9, 18, 4, 0, 0, 0, east)))
}

// TestCurrentFileReadsTheMediaFile: the RSS path's current file is the
// MediaFile's frozen spec -- revision and source included -- plus the source
// Download's info hash, read by the search worker's own search.CurrentFile,
// not the item's rolled-up quality, which has neither.
func TestCurrentFileReadsTheMediaFile(t *testing.T) {
	ctx := context.Background()
	const ns = "rss"
	web1080 := commonv1.Quality{Name: "WEBDL-1080p", Source: commonv1.SourceWebDL, Resolution: 1080}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
		&catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: "file", Namespace: ns},
			Spec: catalogv1alpha1.MediaFileSpec{
				Path: "/data/tv/x.mkv", Quality: web1080, Revision: commonv1.Revision{Version: 2},
				FormatScore: 30, MatchedFormats: []string{"x265"},
				ImportedFrom: &catalogv1alpha1.ImportSource{DownloadRef: "dl", ReleaseTitle: "Show.S01E01.1080p.WEB-DL.PROPER-GRP"},
			},
		},
		&downloadv1alpha1.Download{
			ObjectMeta: metav1.ObjectMeta{Name: "dl", Namespace: ns},
			Spec:       downloadv1alpha1.DownloadSpec{Release: commonv1.ReleaseInfo{InfoHash: "abc123"}},
		},
	).Build()

	cur, err := currentFile(ctx, c, ns, true, ptr.To("file"))
	require.NoError(t, err)
	require.NotNil(t, cur)
	assert.Equal(t, web1080, cur.Quality)
	assert.Equal(t, commonv1.Revision{Version: 2}, cur.Revision, "the revision is what makes a PROPER an upgrade, or not")
	assert.Equal(t, 30, cur.FormatScore)
	assert.Equal(t, []string{"x265"}, cur.Formats)
	assert.Equal(t, "Show.S01E01.1080p.WEB-DL.PROPER-GRP", cur.SourceTitle)
	assert.Equal(t, "abc123", cur.SourceHash, "the info hash comes from the Download that produced the file")

	for name, c2 := range map[string]struct {
		hasFile bool
		ref     *string
	}{
		"no file":                       {false, ptr.To("file")},
		"a file with no ref":            {true, nil},
		"a ref to a MediaFile now gone": {true, ptr.To("gone")},
	} {
		cur, err := currentFile(ctx, c, ns, c2.hasFile, c2.ref)
		require.NoError(t, err, name)
		assert.Nil(t, cur, name)
	}
}
