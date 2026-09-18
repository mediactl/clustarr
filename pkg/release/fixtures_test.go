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
	AirDate       string   `json:"airDate"`
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
	files, err := filepath.Glob("../../testdata/releases/*.json")
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
				if fx.Want.FullSeason {
					assert.True(t, p.FullSeason)
				}
				if fx.Want.MultiSeason {
					assert.True(t, p.MultiSeason)
				}
				if fx.Want.Partial {
					assert.True(t, p.Partial)
				}
				if fx.Want.AirDate != "" {
					require.NotNil(t, p.AirDate)
					assert.Equal(t, fx.Want.AirDate, p.AirDate.Format("2006-01-02"))
				}
			})
		}
	}
	assert.GreaterOrEqual(t, total, 120, "fixture corpus must cover at least 120 titles per the plan")
}
