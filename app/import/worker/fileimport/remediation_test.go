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
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
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
		want downloadv1alpha1.ImportRejectionClass
	}{
		{"nothing walked holds no media: the release's fault", nil, downloadv1alpha1.ImportClassReleaseFault},
		{"only a promo clip is no media either", []rejection{incidental}, downloadv1alpha1.ImportClassReleaseFault},
		{"a release fault alone", []rejection{fault, incidental}, downloadv1alpha1.ImportClassReleaseFault},
		{"the item's state outweighs a fault", []rejection{fault, item}, downloadv1alpha1.ImportClassItemState},
		{"a person outweighs the item's state", []rejection{item, person}, downloadv1alpha1.ImportClassNeedsPerson},
		{"a transient failure outweighs everything", []rejection{fault, person, transient}, downloadv1alpha1.ImportClassTransient},
	} {
		t.Run(c.name, func(t *testing.T) { assert.Equal(t, c.want, classify(c.rs)) })
	}
}

// TestVerdictRejectionBlamesOnlyAMislabelledRelease: a file that is no
// upgrade is the item's state, unless it is of a worse tier than the
// release advertised -- a "2160p" name on a 1080p stream.
func TestVerdictRejectionBlamesOnlyAMislabelledRelease(t *testing.T) {
	uhd, ok := quality.Lookup("video", "Bluray-2160p")
	require.True(t, ok)
	hd, ok := quality.Lookup("video", "Bluray-1080p")
	require.True(t, ok)
	p := quality.Profile{Tiers: [][]quality.Definition{{uhd}, {hd}}}
	advertising := func(q commonv1.Quality) *downloadv1alpha1.Download {
		return &downloadv1alpha1.Download{Spec: downloadv1alpha1.DownloadSpec{Release: commonv1.ReleaseInfo{Quality: q}}}
	}

	r := verdictRejection(p, advertising(uhd.Quality), hd.Quality, "a.mkv: not an upgrade")
	assert.Equal(t, downloadv1alpha1.ImportClassReleaseFault, r.class)
	assert.Contains(t, r.text, "the release advertised Bluray-2160p, the file is Bluray-1080p")

	for _, c := range []struct {
		name               string
		advertised, actual commonv1.Quality
	}{
		{"as advertised", hd.Quality, hd.Quality},
		{"better than advertised", hd.Quality, uhd.Quality},
		{"nothing advertised", commonv1.Quality{}, hd.Quality},
	} {
		r := verdictRejection(p, advertising(c.advertised), c.actual, "a.mkv: not an upgrade")
		assert.Equal(t, downloadv1alpha1.ImportClassItemState, r.class, c.name)
		assert.Equal(t, "a.mkv: not an upgrade", r.text, c.name)
	}
}

// TestASuspectedSampleDecidesOnlyAlone: beside real media it is the
// release's promo clip; alone, a person must say whether it is a short film.
func TestASuspectedSampleDecidesOnlyAlone(t *testing.T) {
	sample := needsPersonRejection("clip.mkv: suspected sample")
	sample.sample = true

	alone := importOutcome{rejections: []rejection{sample}}
	alone.sampleIsIncidental(0)
	assert.Equal(t, downloadv1alpha1.ImportClassNeedsPerson, classify(alone.rejections))

	beside := importOutcome{rejections: []rejection{sample, itemStateRejection("movie.mkv: not an upgrade")}}
	beside.sampleIsIncidental(1)
	assert.Equal(t, downloadv1alpha1.ImportClassItemState, classify(beside.rejections))
}

// TestRetriesFollowTheImportConsumersBackoff: the owner's three tries over
// about an hour, which status.import.nextAttemptAt reports.
func TestRetriesFollowTheImportConsumersBackoff(t *testing.T) {
	assert.Equal(t, 4, maxDeliver(), "a first walk and three retries")
	for attempt, want := range map[uint64]time.Duration{1: time.Minute, 2: 10 * time.Minute, 3: 45 * time.Minute, 9: 45 * time.Minute} {
		assert.Equal(t, want, retryDelay(attempt), "after attempt %d", attempt)
	}
}

// TestOutcomeMessageNamesTheClass: grabarr reads the class, but the message
// is what a person reads, and the two exact constants stay where they were.
func TestOutcomeMessageNamesTheClass(t *testing.T) {
	transcoded := itemStateRejection("a.mkv: existing file is transcoded")
	transcoded.transcoded = true
	assert.Equal(t, downloadv1alpha1.ImportMessageExistingFileFinal, outcomeMessage([]rejection{transcoded}, "none"))
	assert.Equal(t, downloadv1alpha1.ImportMessageEveryFileRejected,
		outcomeMessage([]rejection{releaseFaultRejection("a.mkv: CAM")}, "none"))
	assert.Equal(t, "none", outcomeMessage(nil, "none"))
	assert.Contains(t, outcomeMessage([]rejection{needsPersonRejection("a.mkv: ?")}, "none"), AnnotationImportOverride)
}
