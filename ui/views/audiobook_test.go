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

package views

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/ui/projection"
)

// The audiobook page's Status fact says a file's state in the movie page's
// words, the cutoff reason the catalog recorded after Cutoff Unmet; a
// recording without a file reads its status cell's word.
func TestAudiobookStatusLabel(t *testing.T) {
	file := RowStatus{Kind: "file", Label: "MP3"}
	warn := RowStatus{Kind: "file", Label: "MP3", Warn: true}
	for name, tc := range map[string]struct {
		status       RowStatus
		cutoffReason string
		want         string
	}{
		"downloaded":                 {file, "CutoffMet", "Downloaded"},
		"cutoff unmet":               {warn, "CutoffUnmet", "Cutoff Unmet"},
		"cutoff unmet with a reason": {warn, "BitrateOutOfRange", "Cutoff Unmet: BitrateOutOfRange"},
		"missing":                    {RowStatus{Kind: "missing", Label: "Missing"}, "Pending", "Missing"},
		"unreleased":                 {RowStatus{Kind: "unreleased", Label: "Unreleased"}, "Pending", "Unreleased"},
	} {
		assert.Equal(t, tc.want, audiobookStatusLabel(AudiobookView{Status: tc.status, CutoffReason: tc.cutoffReason}), name)
	}
}

// A release's bitrate is estimated from its size over the audiobook's
// runtime, since an indexer never reports one; nothing without either.
func TestAudiobookBitrateIsEstimatedFromSizeOverRuntime(t *testing.T) {
	assert.Equal(t, "195 kbps", audiobookBitrate(300<<20, 215))
	assert.Equal(t, "64 kbps", audiobookBitrate(64*1000*60*60/8, 60))
	assert.Equal(t, "", audiobookBitrate(300<<20, 0), "no runtime")
	assert.Equal(t, "", audiobookBitrate(0, 215), "no size")
	assert.Equal(t, "", audiobookBitrate(100, 215), "under a kilobit")
}

// The Parts fact is "n of m", the parts on disk over the files the status
// names, whichever is larger the whole; the modal's heading names the
// first author, the title and the subtitle; the Chapters trigger counts.
func TestAudiobookLabels(t *testing.T) {
	assert.Equal(t, "3 of 3", audiobookPartsLabel(3, 3))
	assert.Equal(t, "2 of 3", audiobookPartsLabel(2, 3))
	assert.Equal(t, "3 of 3", audiobookPartsLabel(3, 2), "a status naming fewer files than parts")

	v := AudiobookView{Detail: Detail{Item: projection.LibraryItem{Ref: types.NamespacedName{Namespace: "default", Name: "ab"}, Title: "Dune"}}}
	assert.Equal(t, "Dune", audiobookHeading(v))
	v.Subtitle = "Book One"
	assert.Equal(t, "Dune: Book One", audiobookHeading(v))
	v.Authors = []string{"Frank Herbert", "Someone Else"}
	assert.Equal(t, "Frank Herbert - Dune: Book One", audiobookHeading(v))

	assert.Equal(t, "Show 1 chapter", audiobookChaptersLabel(1))
	assert.Equal(t, "Show 12 chapters", audiobookChaptersLabel(12))
	assert.Equal(t, "Unabridged", abridgedLabel(false))
	assert.Equal(t, "Abridged", abridgedLabel(true))

	p := AudiobookSearchPanel{SearchPanel: SearchPanel{Namespace: "default", Name: "ab-x1"}, Audiobook: "ab"}
	assert.Equal(t, "/library/default/audiobook/ab/search/panel/ab-x1", p.PollURL())
	assert.Equal(t, "/library/default/audiobook/ab/search/panel/ab-x1/grab", p.GrabURL())
	assert.Equal(t, "/library/default/audiobook/ab/details", v.DetailsURL())
}
