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

package release

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSeriesDailyExtractsAirDate(t *testing.T) {
	title := "The.Daily.Show.2018.10.12.Trevor.Noah.720p.WEB.x264-CookieMonster"
	p, err := parseSeries(title, Options{SeriesType: "daily"})
	require.NoError(t, err)
	require.NotNil(t, p.AirDate)
	assert.Equal(t, "2018-10-12", p.AirDate.Format("2006-01-02"))
	assert.Empty(t, p.Seasons)
	assert.Empty(t, p.Episodes)
}
