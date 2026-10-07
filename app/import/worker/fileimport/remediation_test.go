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

package fileimport

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
		transient  = transientRejection("a.mkv: could not be read")
		incidental = incidentalRejection("b.mkv: already has a file from this download")
	)
	for _, c := range []struct {
		name string
		rs   []rejection
		want commonv1.ImportRejectionClass
	}{
		{"nothing walked holds no media: the release's fault", nil, commonv1.ImportClassReleaseFault},
		{"only a promo clip is no media either", []rejection{incidental}, commonv1.ImportClassReleaseFault},
		{"a release fault alone", []rejection{fault, incidental}, commonv1.ImportClassReleaseFault},
		{"the item's state outweighs a fault", []rejection{fault, item}, commonv1.ImportClassItemState},
		{"a person outweighs the item's state", []rejection{item, person}, commonv1.ImportClassNeedsPerson},
		{"a transient failure outweighs everything", []rejection{fault, person, transient}, commonv1.ImportClassTransient},
	} {
		t.Run(c.name, func(t *testing.T) { assert.Equal(t, c.want, classify(c.rs)) })
	}
}

// TestASuspectedSampleDecidesOnlyAlone: beside real media it is the
// release's promo clip; alone, a person must say whether it is a short film.
func TestASuspectedSampleDecidesOnlyAlone(t *testing.T) {
	sample := needsPersonRejection("clip.mkv: suspected sample")
	sample.sample = true

	alone := importOutcome{rejections: []rejection{sample}}
	alone.sampleIsIncidental(0)
	assert.Equal(t, commonv1.ImportClassNeedsPerson, classify(alone.rejections))

	beside := importOutcome{rejections: []rejection{sample, itemStateRejection("movie.mkv: not an upgrade")}}
	beside.sampleIsIncidental(1)
	assert.Equal(t, commonv1.ImportClassItemState, classify(beside.rejections))
}

// TestRetriesFollowTheImportConsumersBackoff: the owner's three tries over
// about an hour, which status.import.nextAttemptAt reports.
func TestRetriesFollowTheImportConsumersBackoff(t *testing.T) {
	assert.Equal(t, 4, maxDeliver(), "a first walk and three retries")
	for attempt, want := range map[uint64]time.Duration{1: time.Minute, 2: 10 * time.Minute, 3: 45 * time.Minute, 9: 45 * time.Minute} {
		assert.Equal(t, want, retryDelay(attempt), "after attempt %d", attempt)
	}
}
