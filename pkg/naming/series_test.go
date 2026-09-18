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

package naming_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/naming"
)

func TestSeasonFolderPadsAndNamesSpecialsPerDialect(t *testing.T) {
	tests := []struct {
		dialect naming.Dialect
		season  int
		want    string
	}{
		{naming.DialectJellyfin, 3, "Season 03"},
		{naming.DialectJellyfin, 0, "Season 00"},
		{naming.DialectPlex, 0, "Season 00"},
		{naming.DialectKodi, 0, "Specials"},
	}
	for _, tt := range tests {
		e := naming.NewEngine(naming.Config{Dialect: tt.dialect})
		got, err := e.SeasonFolder(naming.Context{Season: tt.season})
		require.NoError(t, err)
		require.Equal(t, tt.want, got)
	}
}

func TestSeriesFolderJellyfinIDTag(t *testing.T) {
	e := naming.NewEngine(naming.Config{Dialect: naming.DialectJellyfin})
	c := naming.Context{SeriesTitle: "The Series Title!", SeriesYear: 2010, TvdbID: "153021"}
	got, err := e.SeriesFolder(c)
	require.NoError(t, err)
	require.Equal(t, "The Series Title! (2010) [tvdbid-153021]", got)
}
