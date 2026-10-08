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

package importplan

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// TestClassifyHoldsOnAnyDoubtAndCondemnsOnlyAReleaseFault: of a download
// that imported nothing, any file that could not be judged, needs a person
// or met the item's state holds the download; only a release whose every
// decisive file is at fault, or one holding no media at all, is condemned.
func TestClassifyHoldsOnAnyDoubtAndCondemnsOnlyAReleaseFault(t *testing.T) {
	var (
		fault      = releaseFaultRejection("a.mkv: quality CAM is not allowed by the quality profile")
		item       = itemStateRejection("a.mkv: not an upgrade")
		person     = needsPersonRejection("a.mkv: could not parse the filename")
		transient  = rejection(commonv1.ImportClassTransient, "a.mkv: could not be read")
		incidental = incidentalRejection("b.mkv: already has a file from this download")
	)
	for _, c := range []struct {
		name string
		rs   []Rejection
		want commonv1.ImportRejectionClass
	}{
		{"nothing walked holds no media: the release's fault", nil, commonv1.ImportClassReleaseFault},
		{"only a promo clip is no media either", []Rejection{incidental}, commonv1.ImportClassReleaseFault},
		{"a release fault alone", []Rejection{fault, incidental}, commonv1.ImportClassReleaseFault},
		{"the item's state outweighs a fault", []Rejection{fault, item}, commonv1.ImportClassItemState},
		{"a person outweighs the item's state", []Rejection{item, person}, commonv1.ImportClassNeedsPerson},
		{"a transient failure outweighs everything", []Rejection{fault, person, transient}, commonv1.ImportClassTransient},
	} {
		t.Run(c.name, func(t *testing.T) { assert.Equal(t, c.want, Classify(c.rs)) })
	}
}

// TestRetriesFollowTheImportConsumersBackoff: the owner's three tries over
// about an hour, which the summary's nextAttemptAt reports (A3.8: by
// summary.attempts, no longer the import consumer's redelivery).
func TestRetriesFollowTheImportConsumersBackoff(t *testing.T) {
	assert.Equal(t, 4, len(RetryLadder)+1, "a first inspect and three retries")
	for attempt, want := range map[int]time.Duration{1: time.Minute, 2: 10 * time.Minute, 3: 45 * time.Minute} {
		assert.Equal(t, want, RetryLadder[attempt-1], "after attempt %d", attempt)
	}
}
