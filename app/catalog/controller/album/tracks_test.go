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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/album"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// officialOnly is MusicMetadataProfile's CRD default for ReleaseStatuses.
var officialOnly = catalogv1alpha1.MusicMetadataProfile{ReleaseStatuses: []string{"official"}}

// release builds a MusicBrainz release as pkg/metadata/clients/musicbrainz
// maps one: the web service's own status spelling, one medium, one track
// per recording.
func release(id, status string, recordings ...string) pkgmetadata.AlbumRelease {
	tracks := make([]pkgmetadata.Track, 0, len(recordings))
	for i, rec := range recordings {
		tracks = append(tracks, pkgmetadata.Track{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: rec}, Position: int32(i + 1)})
	}
	return pkgmetadata.AlbumRelease{
		IDs:        pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRelease: id},
		Status:     status,
		TrackCount: int32(len(recordings)),
		Media:      []pkgmetadata.Medium{{Position: 1, Tracks: tracks}},
	}
}

func selectedID(t *testing.T, rel *pkgmetadata.AlbumRelease) string {
	t.Helper()
	require.NotNil(t, rel)
	return rel.IDs[pkgmetadata.KeyMBRelease]
}

func TestSelectReleaseNoReleases(t *testing.T) {
	got, sel := album.SelectRelease(catalogv1alpha1.AlbumSpec{}, officialOnly, "", nil, nil)
	assert.Nil(t, got)
	assert.Equal(t, album.SelectionNoReleases, sel)
}

func TestSelectReleasePinnedAndPresent(t *testing.T) {
	releases := []pkgmetadata.AlbumRelease{release("rel-1", "Official", "a"), release("rel-2", "Bootleg", "a")}
	pinned := "rel-2"
	got, sel := album.SelectRelease(catalogv1alpha1.AlbumSpec{ReleaseID: &pinned}, officialOnly, "rel-1", releases, nil)
	assert.Equal(t, "rel-2", selectedID(t, got), "a pin wins even over a status the profile does not accept")
	assert.Equal(t, album.SelectionPinned, sel)
}

func TestSelectReleasePinnedAbsentFallsBackWhenAnyReleaseOk(t *testing.T) {
	releases := []pkgmetadata.AlbumRelease{release("rel-1", "Official", "a")}
	pinned := "does-not-exist"
	got, sel := album.SelectRelease(catalogv1alpha1.AlbumSpec{ReleaseID: &pinned, AnyReleaseOk: ptr.To(true)}, officialOnly, "", releases, nil)
	assert.Equal(t, "rel-1", selectedID(t, got))
	assert.Equal(t, album.SelectionBest, sel)
}

func TestSelectReleasePinnedAbsentReportsNoneWhenAnyReleaseOkIsFalse(t *testing.T) {
	releases := []pkgmetadata.AlbumRelease{release("rel-1", "Official", "a")}
	pinned := "does-not-exist"
	got, sel := album.SelectRelease(catalogv1alpha1.AlbumSpec{ReleaseID: &pinned, AnyReleaseOk: ptr.To(false)}, officialOnly, "", releases, nil)
	assert.Nil(t, got, "a user who pinned a release and declined others gets nothing rather than a guessed substitute")
	assert.Equal(t, album.SelectionPinnedReleaseMissing, sel)
}

// TestSelectReleaseHonoursReleaseStatuses: only releases whose status the
// profile accepts are candidates, spelled as MusicBrainz spells them.
func TestSelectReleaseHonoursReleaseStatuses(t *testing.T) {
	releases := []pkgmetadata.AlbumRelease{
		release("promo", "Promotion", "a", "b", "c"),
		release("official", "Official", "a", "b"),
	}
	got, sel := album.SelectRelease(catalogv1alpha1.AlbumSpec{}, officialOnly, "", releases, nil)
	assert.Equal(t, "official", selectedID(t, got), "the promo has more tracks, but the profile accepts only official releases")
	assert.Equal(t, album.SelectionBest, sel)

	withPromo := catalogv1alpha1.MusicMetadataProfile{ReleaseStatuses: []string{"official", "promotion"}}
	got, _ = album.SelectRelease(catalogv1alpha1.AlbumSpec{}, withPromo, "", releases, nil)
	assert.Equal(t, "promo", selectedID(t, got), "once promotions are accepted, most tracks wins")
}

func TestSelectReleaseNoAcceptedRelease(t *testing.T) {
	releases := []pkgmetadata.AlbumRelease{release("boot", "Bootleg", "a"), release("pseudo", "Pseudo-Release", "a")}
	got, sel := album.SelectRelease(catalogv1alpha1.AlbumSpec{}, officialOnly, "", releases, nil)
	assert.Nil(t, got)
	assert.Equal(t, album.SelectionNoAcceptedRelease, sel)
}

// TestSelectReleaseSkipsTracklessReleases is SkyHookProxy.MapAlbum's
// `.Where(x => x.TrackCount > 0)`.
func TestSelectReleaseSkipsTracklessReleases(t *testing.T) {
	empty := release("empty", "Official")
	releases := []pkgmetadata.AlbumRelease{empty, release("full", "Official", "a")}
	got, _ := album.SelectRelease(catalogv1alpha1.AlbumSpec{}, officialOnly, "", releases, nil)
	assert.Equal(t, "full", selectedID(t, got))

	got, sel := album.SelectRelease(catalogv1alpha1.AlbumSpec{}, officialOnly, "", []pkgmetadata.AlbumRelease{empty}, nil)
	assert.Nil(t, got)
	assert.Equal(t, album.SelectionNoAcceptedRelease, sel)
}

// TestSelectReleaseMostFilesThenMostTracks is MonitorSingleRelease's
// OrderByDescending(files).ThenByDescending(TrackCount), ties to the
// provider's order.
func TestSelectReleaseMostFilesThenMostTracks(t *testing.T) {
	cd := release("cd", "Official", "a", "b")
	deluxe := release("deluxe", "Official", "a", "b", "c", "d")
	vinyl := release("vinyl", "Official", "x", "y")
	releases := []pkgmetadata.AlbumRelease{cd, deluxe, vinyl}

	got, sel := album.SelectRelease(catalogv1alpha1.AlbumSpec{}, officialOnly, "", releases, nil)
	assert.Equal(t, "deluxe", selectedID(t, got), "no files anywhere: most tracks")
	assert.Equal(t, album.SelectionBest, sel)

	files := map[string]string{"x": "mf-x", "y": "mf-y"}
	got, _ = album.SelectRelease(catalogv1alpha1.AlbumSpec{}, officialOnly, "", releases, files)
	assert.Equal(t, "vinyl", selectedID(t, got), "the release holding the files wins over one with more tracks")

	twin := release("twin", "Official", "a", "b")
	got, _ = album.SelectRelease(catalogv1alpha1.AlbumSpec{}, officialOnly, "", []pkgmetadata.AlbumRelease{cd, twin}, nil)
	assert.Equal(t, "cd", selectedID(t, got), "a full tie goes to the provider's order")
}

// TestSelectReleaseKeepsThePreviousSelection is MonitorSingleRelease keeping
// the monitored release: a release with more tracks appearing later does not
// displace it, and only more files do, and only while anyReleaseOk.
func TestSelectReleaseKeepsThePreviousSelection(t *testing.T) {
	cd := release("cd", "Official", "a", "b")
	deluxe := release("deluxe", "Official", "a", "b", "c", "d")
	vinyl := release("vinyl", "Official", "x", "y")
	releases := []pkgmetadata.AlbumRelease{cd, deluxe, vinyl}

	got, sel := album.SelectRelease(catalogv1alpha1.AlbumSpec{}, officialOnly, "cd", releases, nil)
	assert.Equal(t, "cd", selectedID(t, got), "more tracks elsewhere does not move an existing selection")
	assert.Equal(t, album.SelectionKept, sel)

	files := map[string]string{"a": "mf-a"}
	got, sel = album.SelectRelease(catalogv1alpha1.AlbumSpec{}, officialOnly, "cd", releases, files)
	assert.Equal(t, "cd", selectedID(t, got), "deluxe holds the same one file: a tie keeps the selection")
	assert.Equal(t, album.SelectionKept, sel)

	files = map[string]string{"x": "mf-x"}
	got, sel = album.SelectRelease(catalogv1alpha1.AlbumSpec{}, officialOnly, "cd", releases, files)
	assert.Equal(t, "vinyl", selectedID(t, got), "with anyReleaseOk, strictly more files moves the selection")
	assert.Equal(t, album.SelectionBest, sel)

	got, sel = album.SelectRelease(catalogv1alpha1.AlbumSpec{AnyReleaseOk: ptr.To(false)}, officialOnly, "cd", releases, files)
	assert.Equal(t, "cd", selectedID(t, got), "without anyReleaseOk the selection stays put")
	assert.Equal(t, album.SelectionKept, sel)

	got, _ = album.SelectRelease(catalogv1alpha1.AlbumSpec{}, officialOnly, "withdrawn-since", releases, nil)
	assert.Equal(t, "deluxe", selectedID(t, got), "a previous selection that is no longer a candidate is forgotten")
}

func TestFilesByRecording(t *testing.T) {
	older := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	newer := metav1.NewTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	mf := func(name, track string, created metav1.Time) catalogv1alpha1.MediaFile {
		return catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: created},
			Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "ok-computer", Track: track}},
		}
	}
	files := album.FilesByRecording([]catalogv1alpha1.MediaFile{
		mf("whole-album", "", newer),
		mf("airbag-old", "rec-1", older),
		mf("airbag-new", "rec-1", newer),
		mf("paranoid", "rec-2", older),
	})
	assert.Equal(t, map[string]string{"rec-1": "airbag-new", "rec-2": "paranoid"}, files,
		"a whole-album file addresses no track; two claims on one recording resolve as rollup.PickMediaFile does")
}

func TestBuildTracksSetsFileRefAndKeepsARepeatedRecordingOnce(t *testing.T) {
	rel := &pkgmetadata.AlbumRelease{Media: []pkgmetadata.Medium{
		{Position: 1, Tracks: []pkgmetadata.Track{
			{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: "rec-1"}, Title: "Airbag", Position: 1},
			{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: "rec-2"}, Title: "Paranoid Android", Position: 2},
		}},
		// The same audio again on a second medium (a CD plus its DVD-Audio).
		{Position: 2, Tracks: []pkgmetadata.Track{
			{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: "rec-1"}, Title: "Airbag", Position: 1},
		}},
	}}
	tracks, truncated := album.BuildTracks(rel, map[string]string{"rec-2": "paranoid-mf"})
	assert.False(t, truncated)
	require.Len(t, tracks, 2, "status.tracks is keyed by recordingID; a repeat would be refused by the apiserver")
	assert.EqualValues(t, 1, *tracks[0].Medium, "the first occurrence is kept")
	assert.Nil(t, tracks[0].FileRef)
	require.NotNil(t, tracks[1].FileRef)
	assert.Equal(t, "paranoid-mf", *tracks[1].FileRef)
}

func TestTracksFromStatusRefreshesFileRefs(t *testing.T) {
	existing := []catalogv1alpha1.Track{
		{RecordingID: "rec-1", Medium: 1, Number: 1, AbsoluteNumber: 1, Title: "Airbag", DurationMs: 284000, FileRef: ptr.To("gone")},
		{RecordingID: "rec-2", Medium: 1, Number: 2, AbsoluteNumber: 2, Title: "Paranoid Android"},
	}
	tracks := album.TracksFromStatus(existing, map[string]string{"rec-2": "paranoid-mf"})
	require.Len(t, tracks, 2)
	assert.Equal(t, "Airbag", *tracks[0].Title)
	assert.EqualValues(t, 284000, *tracks[0].DurationMs)
	assert.Nil(t, tracks[0].FileRef, "a file that no longer exists is dropped")
	require.NotNil(t, tracks[1].FileRef)
	assert.Equal(t, "paranoid-mf", *tracks[1].FileRef)
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

	tracks, truncated := album.BuildTracks(rel, nil)
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
	tracks, truncated := album.BuildTracks(rel, nil)
	require.Len(t, tracks, 1)
	assert.False(t, truncated)
	assert.Equal(t, "rec-1", *tracks[0].RecordingID)
}

func TestBuildTracksNilRelease(t *testing.T) {
	tracks, truncated := album.BuildTracks(nil, nil)
	assert.Nil(t, tracks)
	assert.False(t, truncated)
}

func TestBuildTracksReportsTruncationBeyond200(t *testing.T) {
	tracksIn := make([]pkgmetadata.Track, 0, 201)
	for i := 0; i < 201; i++ {
		tracksIn = append(tracksIn, pkgmetadata.Track{
			IDs:      pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: fmt.Sprintf("rec-%d", i)},
			Position: int32(i + 1),
		})
	}
	rel := &pkgmetadata.AlbumRelease{Media: []pkgmetadata.Medium{{Position: 1, Tracks: tracksIn}}}

	tracks, truncated := album.BuildTracks(rel, nil)
	assert.Len(t, tracks, 200, "AlbumStatus.Tracks' own +kubebuilder:validation:MaxItems=200")
	assert.True(t, truncated, "a release with more than 200 tracks must raise Invalid, not silently truncate")
}
