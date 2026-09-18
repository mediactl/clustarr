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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

func TestNewTargetDispatchesMovieAndSeriesOnly(t *testing.T) {
	m, err := newTarget(commonv1.MediaKindMovie)
	require.NoError(t, err)
	require.IsType(t, &catalogv1alpha1.Movie{}, m)

	s, err := newTarget(commonv1.MediaKindSeries)
	require.NoError(t, err)
	require.IsType(t, &catalogv1alpha1.Series{}, s)

	_, err = newTarget(commonv1.MediaKindArtist)
	require.True(t, errors.Is(err, errUnsupportedKind), "artist is M6 scope, see run.go's own TODO(M6)")
}

func TestExternalIDsReadsTheSpecID(t *testing.T) {
	movie := &catalogv1alpha1.Movie{Spec: catalogv1alpha1.MovieSpec{TmdbID: 27205}}
	ids, err := externalIDs(movie)
	require.NoError(t, err)
	require.Equal(t, pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: "27205"}, ids)

	series := &catalogv1alpha1.Series{Spec: catalogv1alpha1.SeriesSpec{TvdbID: 121361}}
	ids, err = externalIDs(series)
	require.NoError(t, err)
	require.Equal(t, pkgmetadata.ExternalIDs{pkgmetadata.KeyTVDB: "121361"}, ids)
}

func TestRefreshedAtIsZeroBeforeTheFirstFetch(t *testing.T) {
	require.True(t, refreshedAt(&catalogv1alpha1.Movie{}).IsZero())

	when := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	m := &catalogv1alpha1.Movie{Status: catalogv1alpha1.MovieStatus{
		Metadata: &catalogv1alpha1.MovieMetadata{RefreshedAt: when},
	}}
	require.True(t, refreshedAt(m).Equal(when.Time))
}

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
