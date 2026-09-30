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

package markers_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/markers"
)

func file(kind commonv1.MediaKind, probed bool, m *catalogv1alpha1.FileMarkers) *catalogv1alpha1.MediaFile {
	mf := &catalogv1alpha1.MediaFile{}
	mf.Spec.MediaRef = commonv1.MediaRef{Kind: kind, Name: "item"}
	if probed {
		mf.Status.ProbeHash = "hash-1"
		mf.Status.MediaInfo = &commonv1.MediaInfo{RuntimeMillis: 2138069}
	}
	mf.Status.Markers = m
	return mf
}

func fetched(result catalogv1alpha1.MarkersResult, hash string, ago time.Duration, now time.Time) *catalogv1alpha1.FileMarkers {
	return &catalogv1alpha1.FileMarkers{Result: result, ForProbeHash: hash, FetchedAt: metav1.NewTime(now.Add(-ago))}
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	tests := []struct {
		name      string
		mf        *catalogv1alpha1.MediaFile
		due       bool
		recheckIn time.Duration
	}{
		{"an unprobed file waits for its probe", file(commonv1.MediaKindEpisode, false, nil), false, 0},
		{"an album has no markers", file(commonv1.MediaKindAlbum, true, nil), false, 0},
		{"a probed episode never fetched", file(commonv1.MediaKindEpisode, true, nil), true, 0},
		{"a probed movie never fetched", file(commonv1.MediaKindMovie, true, nil), true, 0},
		{"a new file at the same path", file(commonv1.MediaKindMovie, true, fetched(catalogv1alpha1.MarkersFound, "hash-0", time.Hour, now)), true, 0},
		{"found an hour ago", file(commonv1.MediaKindMovie, true, fetched(catalogv1alpha1.MarkersFound, "hash-1", time.Hour, now)), false, 30*day - time.Hour},
		{"found 30 days ago", file(commonv1.MediaKindMovie, true, fetched(catalogv1alpha1.MarkersFound, "hash-1", 30*day, now)), true, 0},
		{"not found 6 days ago", file(commonv1.MediaKindEpisode, true, fetched(catalogv1alpha1.MarkersNotFound, "hash-1", 6*day, now)), false, day},
		{"not found 7 days ago", file(commonv1.MediaKindEpisode, true, fetched(catalogv1alpha1.MarkersNotFound, "hash-1", 7*day, now)), true, 0},
		{"an error 2 hours ago", file(commonv1.MediaKindEpisode, true, fetched(catalogv1alpha1.MarkersError, "hash-1", 2*time.Hour, now)), false, 22 * time.Hour},
		{"an error a day ago", file(commonv1.MediaKindEpisode, true, fetched(catalogv1alpha1.MarkersError, "hash-1", day, now)), true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			due, in := markers.Due(tt.mf, now)
			assert.Equal(t, tt.due, due)
			assert.Equal(t, tt.recheckIn, in)
		})
	}
}

// Each fetch of a file has its own message id: the file and the fetch it
// replaces, so a re-fetch 30 days on is not absorbed as a duplicate of the
// first, and a republish of the same pending fetch is.
func TestMsgIDNamesTheFileAndTheFetchItReplaces(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	fresh := file(commonv1.MediaKindMovie, true, nil)
	fresh.UID = "uid-1"
	assert.Equal(t, "uid-1:0:markers-hash-1-0", markers.MsgID(fresh))
	again := file(commonv1.MediaKindMovie, true, fetched(catalogv1alpha1.MarkersFound, "hash-1", 31*24*time.Hour, now))
	again.UID = "uid-1"
	assert.NotEqual(t, markers.MsgID(fresh), markers.MsgID(again))
}
