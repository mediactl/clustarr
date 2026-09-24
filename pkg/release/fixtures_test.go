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

package release_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

type fixtureWant struct {
	Title         string   `json:"title"`
	Year          int      `json:"year"`
	QualityName   string   `json:"qualityName"`
	Group         string   `json:"group"`
	Edition       string   `json:"edition"`
	ProperVersion int32    `json:"properVersion"`
	Repack        bool     `json:"repack"`
	Languages     []string `json:"languages"`
	Seasons       []int    `json:"seasons"`
	Episodes      []int    `json:"episodes"`
	Absolute      []int    `json:"absolute"`
	FullSeason    bool     `json:"fullSeason"`
	MultiSeason   bool     `json:"multiSeason"`
	Partial       bool     `json:"partial"`
	Special       bool     `json:"special"`
	AirDate       string   `json:"airDate"`

	// Sub-field coverage (fix round item 6): omitted (zero value) means not
	// asserted, same convention as every field above. comicVolume is the
	// one exception worth calling out — 0 is also ComicInfo.Volume's
	// legitimate "not a manga volume release" value, so a fixture wanting
	// to assert that explicitly is already covered by comic_test.go's
	// whitebox "western comic" case; the corpus only needs to assert a
	// real (non-zero) volume.
	MusicCodec     string   `json:"musicCodec"`
	BookAuthor     string   `json:"bookAuthor"`
	BookNarrator   string   `json:"bookNarrator"`
	ComicIssue     string   `json:"comicIssue"`
	ComicVolume    int      `json:"comicVolume"`
	HintsCodec     []string `json:"hintsCodec"`
	HintsAudio     []string `json:"hintsAudio"`
	HintsStreaming []string `json:"hintsStreaming"`
}

type fixture struct {
	Title string      `json:"title"`
	Kind  string      `json:"kind"`
	Want  fixtureWant `json:"want"`
}

func loadFixtures(t *testing.T, path string) []fixture {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var fs []fixture
	require.NoError(t, json.Unmarshal(data, &fs))
	require.NotEmpty(t, fs)
	return fs
}

func TestFixtureCorpusParsesToExpectedFields(t *testing.T) {
	files, err := filepath.Glob("../../test/data/releases/*.json")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(files), 10)

	total := 0
	for _, file := range files {
		for _, fx := range loadFixtures(t, file) {
			total++
			t.Run(filepath.Base(file)+"/"+fx.Title, func(t *testing.T) {
				kind := commonv1.MediaKind(fx.Kind)
				p, err := release.ParseKind(fx.Title, kind)
				require.NoError(t, err)
				if fx.Want.Title != "" {
					assert.Equal(t, fx.Want.Title, p.Title)
				}
				if fx.Want.Year != 0 {
					assert.Equal(t, fx.Want.Year, p.Year)
				}
				if fx.Want.QualityName != "" {
					assert.Equal(t, fx.Want.QualityName, p.Quality.Name)
				}
				if fx.Want.Group != "" {
					assert.Equal(t, fx.Want.Group, p.Group)
				}
				if fx.Want.Edition != "" {
					assert.Equal(t, fx.Want.Edition, p.Edition)
				}
				if fx.Want.ProperVersion != 0 {
					assert.Equal(t, fx.Want.ProperVersion, p.Revision.Version)
				}
				if fx.Want.Repack {
					assert.True(t, p.Revision.Repack)
				}
				if len(fx.Want.Languages) > 0 {
					assert.Equal(t, fx.Want.Languages, p.Languages)
				}
				if len(fx.Want.Seasons) > 0 {
					assert.Equal(t, fx.Want.Seasons, p.Seasons)
				}
				if len(fx.Want.Episodes) > 0 {
					assert.Equal(t, fx.Want.Episodes, p.Episodes)
				}
				if len(fx.Want.Absolute) > 0 {
					assert.Equal(t, fx.Want.Absolute, p.Absolute)
				}
				// Exact equality, not "if true assert true": a fixture that
				// omits one of these asserts it is false, so a regex change
				// that starts flagging FullSeason/MultiSeason/Partial/
				// Special on a title that shouldn't have it is caught here
				// instead of silently passing.
				assert.Equal(t, fx.Want.FullSeason, p.FullSeason, "FullSeason")
				assert.Equal(t, fx.Want.MultiSeason, p.MultiSeason, "MultiSeason")
				assert.Equal(t, fx.Want.Partial, p.Partial, "Partial")
				assert.Equal(t, fx.Want.Special, p.Special, "Special")
				if fx.Want.AirDate != "" {
					require.NotNil(t, p.AirDate)
					assert.Equal(t, fx.Want.AirDate, p.AirDate.Format("2006-01-02"))
				}
				if fx.Want.MusicCodec != "" {
					require.NotNil(t, p.Music)
					assert.Equal(t, fx.Want.MusicCodec, p.Music.Codec)
				}
				if fx.Want.BookAuthor != "" {
					require.NotNil(t, p.Book)
					assert.Equal(t, fx.Want.BookAuthor, p.Book.Author)
				}
				if fx.Want.BookNarrator != "" {
					require.NotNil(t, p.Book)
					assert.Equal(t, fx.Want.BookNarrator, p.Book.Narrator)
				}
				if fx.Want.ComicIssue != "" {
					require.NotNil(t, p.Comic)
					assert.Equal(t, fx.Want.ComicIssue, p.Comic.Issue)
				}
				if fx.Want.ComicVolume != 0 {
					require.NotNil(t, p.Comic)
					assert.Equal(t, fx.Want.ComicVolume, p.Comic.Volume)
				}
				if len(fx.Want.HintsCodec) > 0 {
					assert.Equal(t, fx.Want.HintsCodec, p.Hints.Codec)
				}
				if len(fx.Want.HintsAudio) > 0 {
					assert.Equal(t, fx.Want.HintsAudio, p.Hints.Audio)
				}
				if len(fx.Want.HintsStreaming) > 0 {
					assert.Equal(t, fx.Want.HintsStreaming, p.Hints.Streaming)
				}
			})
		}
	}
	assert.GreaterOrEqual(t, total, 120, "fixture corpus must cover at least 120 titles per the plan")
}
