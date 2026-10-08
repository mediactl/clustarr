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

package transcode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// TestCopyTakesTheTranscodeBlockAndItsNoncesOnly is the transcode row of
// §3.5's partition: status.transcode and the two transcode nonce leaves,
// deep-copied; the subtitle nonce and every other field stay the
// destination's.
func TestCopyTakesTheTranscodeBlockAndItsNoncesOnly(t *testing.T) {
	from := &catalogv1alpha1.MediaFileStatus{
		ProbeHash: "p", Transcode: &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhasePlanned,
			Dispatch: &catalogv1alpha1.Dispatch{Seq: 4}},
		HandledNonces: &catalogv1alpha1.HandledNonces{TranscodeRetry: "1", TranscodeCancel: "2", SubtitleSearch: "3"},
	}
	theirs := &catalogv1alpha1.HandledNonces{SubtitleSearch: "9"}
	into := &catalogv1alpha1.MediaFileStatus{ProbeHash: "q", HandledNonces: theirs}
	New(Options{}).Copy(from, into)
	assert.Equal(t, "q", into.ProbeHash)
	require.NotNil(t, into.Transcode)
	assert.Equal(t, catalogv1alpha1.TranscodePhasePlanned, into.Transcode.Phase)
	assert.Equal(t, &catalogv1alpha1.HandledNonces{TranscodeRetry: "1", TranscodeCancel: "2", SubtitleSearch: "9"}, into.HandledNonces)
	assert.Equal(t, &catalogv1alpha1.HandledNonces{SubtitleSearch: "9"}, theirs, "the destination's nonces are copied before they are written")
	from.Transcode.Phase = catalogv1alpha1.TranscodePhaseQueued
	from.Transcode.Dispatch.Seq = 5
	assert.Equal(t, catalogv1alpha1.TranscodePhasePlanned, into.Transcode.Phase, "a deep copy")
	assert.Equal(t, int64(4), into.Transcode.Dispatch.Seq, "a deep copy")

	New(Options{}).Copy(&catalogv1alpha1.MediaFileStatus{}, into)
	assert.Nil(t, into.Transcode)
	assert.Equal(t, &catalogv1alpha1.HandledNonces{SubtitleSearch: "9"}, into.HandledNonces)

	bare := &catalogv1alpha1.MediaFileStatus{HandledNonces: &catalogv1alpha1.HandledNonces{TranscodeRetry: "7"}}
	New(Options{}).Copy(&catalogv1alpha1.MediaFileStatus{}, bare)
	assert.Nil(t, bare.HandledNonces, "an empty nonce record is no record")
	New(Options{}).Copy(&catalogv1alpha1.MediaFileStatus{HandledNonces: &catalogv1alpha1.HandledNonces{TranscodeCancel: "8"}}, bare)
	assert.Equal(t, &catalogv1alpha1.HandledNonces{TranscodeCancel: "8"}, bare.HandledNonces)
}
