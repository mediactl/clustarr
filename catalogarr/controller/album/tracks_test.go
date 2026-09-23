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

package album_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/album"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

func TestSelectReleaseNoReleases(t *testing.T) {
	_, ok := album.SelectRelease(catalogv1alpha1.AlbumSpec{}, nil)
	assert.False(t, ok)
}

func TestSelectReleaseNoPinPicksFirst(t *testing.T) {
	releases := []pkgmetadata.AlbumRelease{
		{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRelease: "rel-1"}},
		{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRelease: "rel-2"}},
	}
	got, ok := album.SelectRelease(catalogv1alpha1.AlbumSpec{}, releases)
	require.True(t, ok)
	assert.Equal(t, "rel-1", got.IDs[pkgmetadata.KeyMBRelease])
}

func TestSelectReleasePinnedAndPresent(t *testing.T) {
	releases := []pkgmetadata.AlbumRelease{
		{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRelease: "rel-1"}},
		{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRelease: "rel-2"}},
	}
	pinned := "rel-2"
	got, ok := album.SelectRelease(catalogv1alpha1.AlbumSpec{ReleaseID: &pinned}, releases)
	require.True(t, ok)
	assert.Equal(t, "rel-2", got.IDs[pkgmetadata.KeyMBRelease])
}

func TestSelectReleasePinnedAbsentFallsBackWhenAnyReleaseOk(t *testing.T) {
	releases := []pkgmetadata.AlbumRelease{{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRelease: "rel-1"}}}
	pinned := "does-not-exist"
	got, ok := album.SelectRelease(catalogv1alpha1.AlbumSpec{ReleaseID: &pinned, AnyReleaseOk: ptr.To(true)}, releases)
	require.True(t, ok)
	assert.Equal(t, "rel-1", got.IDs[pkgmetadata.KeyMBRelease])
}

func TestSelectReleasePinnedAbsentReportsNoneWhenAnyReleaseOkIsFalse(t *testing.T) {
	releases := []pkgmetadata.AlbumRelease{{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRelease: "rel-1"}}}
	pinned := "does-not-exist"
	_, ok := album.SelectRelease(catalogv1alpha1.AlbumSpec{ReleaseID: &pinned, AnyReleaseOk: ptr.To(false)}, releases)
	assert.False(t, ok, "a user who pinned a release and declined others gets nothing rather than a guessed substitute")
}

func TestBuildTracksFlattensMediaInOrderWithCumulativeAbsoluteNumber(t *testing.T) {
	rel := &pkgmetadata.AlbumRelease{
		Media: []pkgmetadata.Medium{
			{
				Position: 1,
				Tracks: []pkgmetadata.Track{
					{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: "rec-1"}, Title: "Airbag", Position: 1, Duration: 4*time.Minute + 44*time.Second},
					{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: "rec-2"}, Title: "Paranoid Android", Position: 2, Duration: 6*time.Minute + 23*time.Second},
				},
			},
			{
				Position: 2,
				Tracks: []pkgmetadata.Track{
					{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: "rec-3"}, Title: "Bonus Track", Position: 1, Duration: 3 * time.Minute},
				},
			},
		},
	}

	tracks, truncated := album.BuildTracks(rel)
	require.Len(t, tracks, 3)
	assert.False(t, truncated)

	first := tracks[0]
	assert.Equal(t, "rec-1", *first.RecordingID)
	assert.EqualValues(t, 1, *first.Medium)
	assert.EqualValues(t, 1, *first.Number)
	assert.EqualValues(t, 1, *first.AbsoluteNumber)
	assert.Equal(t, "Airbag", *first.Title)
	assert.EqualValues(t, (4*time.Minute + 44*time.Second).Milliseconds(), *first.DurationMs)

	third := tracks[2]
	assert.Equal(t, "rec-3", *third.RecordingID)
	assert.EqualValues(t, 2, *third.Medium)
	assert.EqualValues(t, 1, *third.Number)
	assert.EqualValues(t, 3, *third.AbsoluteNumber, "absolute number counts across media, not resetting per disc")
}

func TestBuildTracksDropsRecordingsWithNoRecordingID(t *testing.T) {
	rel := &pkgmetadata.AlbumRelease{
		Media: []pkgmetadata.Medium{{Position: 1, Tracks: []pkgmetadata.Track{
			{Title: "Untagged", Position: 1}, // no IDs at all
			{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: "rec-1"}, Title: "Tagged", Position: 2},
		}}},
	}
	tracks, truncated := album.BuildTracks(rel)
	require.Len(t, tracks, 1)
	assert.False(t, truncated)
	assert.Equal(t, "rec-1", *tracks[0].RecordingID)
}

func TestBuildTracksNilRelease(t *testing.T) {
	tracks, truncated := album.BuildTracks(nil)
	assert.Nil(t, tracks)
	assert.False(t, truncated)
}

func TestBuildTracksReportsTruncationBeyond200(t *testing.T) {
	tracksIn := make([]pkgmetadata.Track, 0, 201)
	for i := 0; i < 201; i++ {
		tracksIn = append(tracksIn, pkgmetadata.Track{
			IDs:      pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: "rec"},
			Position: int32(i + 1),
		})
	}
	rel := &pkgmetadata.AlbumRelease{Media: []pkgmetadata.Medium{{Position: 1, Tracks: tracksIn}}}

	tracks, truncated := album.BuildTracks(rel)
	assert.Len(t, tracks, 200, "AlbumStatus.Tracks' own +kubebuilder:validation:MaxItems=200")
	assert.True(t, truncated, "a release with more than 200 tracks must raise Invalid, not silently truncate")
}
