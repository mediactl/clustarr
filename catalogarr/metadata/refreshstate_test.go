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

package metadata

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

func TestMovieRefreshState(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	recent := now.Add(-10 * 24 * time.Hour)
	old := now.Add(-400 * 24 * time.Hour)

	require.Equal(t, pkgmetadata.RefreshStateAnnounced, movieRefreshState(&pkgmetadata.Movie{Status: pkgmetadata.MovieStatusAnnounced}, now))
	require.Equal(t, pkgmetadata.RefreshStateInCinemas, movieRefreshState(&pkgmetadata.Movie{Status: pkgmetadata.MovieStatusInCinemas}, now))
	require.Equal(t, pkgmetadata.RefreshStateReleasedRecent,
		movieRefreshState(&pkgmetadata.Movie{Status: pkgmetadata.MovieStatusReleased, DigitalRelease: &recent}, now))
	require.Equal(t, pkgmetadata.RefreshStateReleasedOld,
		movieRefreshState(&pkgmetadata.Movie{Status: pkgmetadata.MovieStatusReleased, DigitalRelease: &old}, now))
}

func TestSeriesRefreshState(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	recent := now.Add(-10 * 24 * time.Hour)
	old := now.Add(-400 * 24 * time.Hour)

	require.Equal(t, pkgmetadata.RefreshStateContinuing, seriesRefreshState(&pkgmetadata.Series{Status: pkgmetadata.SeriesStatusContinuing}, now))
	require.Equal(t, pkgmetadata.RefreshStateEndedRecent,
		seriesRefreshState(&pkgmetadata.Series{Status: pkgmetadata.SeriesStatusEnded, LastAired: &recent}, now))
	require.Equal(t, pkgmetadata.RefreshStateEndedOld,
		seriesRefreshState(&pkgmetadata.Series{Status: pkgmetadata.SeriesStatusEnded, LastAired: &old}, now))
	require.Equal(t, pkgmetadata.RefreshStateAnnounced, seriesRefreshState(&pkgmetadata.Series{Status: pkgmetadata.SeriesStatusUpcoming}, now))
}

func TestComicRefreshState(t *testing.T) {
	require.Equal(t, pkgmetadata.RefreshStateCompleted, comicRefreshState(&pkgmetadata.ComicVolume{Status: "ended"}))
	require.Equal(t, pkgmetadata.RefreshStateCompleted, comicRefreshState(&pkgmetadata.ComicVolume{Status: "completed"}))
	require.Equal(t, pkgmetadata.RefreshStateOngoing, comicRefreshState(&pkgmetadata.ComicVolume{Status: "ongoing"}))
	require.Equal(t, pkgmetadata.RefreshStateOngoing, comicRefreshState(&pkgmetadata.ComicVolume{Status: "continuing"}),
		"comicvine.Client.Volume's derived vocabulary is continuing/ended")
	require.Equal(t, pkgmetadata.RefreshStateOngoing, comicRefreshState(&pkgmetadata.ComicVolume{}),
		"an unknown status (the client's latest-issue lookup failed) keeps the conservative daily cadence")
}
