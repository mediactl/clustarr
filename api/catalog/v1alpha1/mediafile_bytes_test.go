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

package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// The hashes a file's bytes had at earlier paths: each path-only change
// appends the one it left, a name the file returns to stops being former,
// and the list keeps its newest four.
func TestWithFormerProbeHash(t *testing.T) {
	assert.Equal(t, []string{"a"}, WithFormerProbeHash(nil, "a", "b"))
	assert.Equal(t, []string{"a", "b"}, WithFormerProbeHash([]string{"a"}, "b", "c"))
	assert.Equal(t, []string{"b"}, WithFormerProbeHash([]string{"a"}, "b", "a"), "renamed back to a name it had")
	assert.Equal(t, []string{"a", "b"}, WithFormerProbeHash([]string{"a", "b"}, "b", "c"), "no hash twice")
	assert.Equal(t, []string{"b", "c", "d", "e"}, WithFormerProbeHash([]string{"a", "b", "c", "d"}, "e", "f"),
		"the newest MaxFormerProbeHashes")
	assert.Len(t, WithFormerProbeHash([]string{"a", "b", "c", "d"}, "e", "f"), MaxFormerProbeHashes)
	assert.Nil(t, WithFormerProbeHash(nil, "", "b"), "a file never probed left no hash")
	assert.Nil(t, WithFormerProbeHash(nil, "b", "b"))
}

func TestSameBytes(t *testing.T) {
	s := &MediaFileStatus{ProbeHash: "now", FormerProbeHashes: []string{"old", "older"}}
	for _, h := range []string{"now", "old", "older"} {
		assert.True(t, s.SameBytes(h), h)
	}
	assert.False(t, s.SameBytes("other"))
	assert.False(t, s.SameBytes(""))
	var none *MediaFileStatus
	assert.False(t, none.SameBytes("now"))
}

// A moved file's blocks follow its bytes to the hash they have now: the
// transcode and graft verdicts, the subtitle history, the markers and the
// segment analysis. A block with work in flight is left to its stage, whose
// task names the old path; a block about other bytes is not this move's.
func TestCarryBytes(t *testing.T) {
	moved := func() *MediaFileStatus {
		return &MediaFileStatus{
			ProbeHash: "now", FormerProbeHashes: []string{"old"},
			Transcode: &TranscodeState{Phase: TranscodePhaseFailed, ProbeHash: "old"},
			Graft:     &GraftState{Phase: GraftPhaseSucceeded, ProbeHash: "old"},
			Subtitles: &SubtitlesStatus{ProbeHash: "old", Items: []SubtitleItemStatus{
				{LangKey: "en", Attempts: commonv1.Attempts{Count: 6}, Dispatch: &Dispatch{Seq: 3, AnsweredSeq: 3}},
			}},
			Markers: &FileMarkers{ForProbeHash: "old", Analysis: &SegmentAnalysis{ForProbeHash: "old"}},
		}
	}

	s := moved()
	require.True(t, s.CarryBytes())
	assert.Equal(t, "now", s.Transcode.ProbeHash)
	assert.Equal(t, TranscodePhaseFailed, s.Transcode.Phase, "the verdict the bytes earned stands")
	assert.Equal(t, "now", s.Graft.ProbeHash)
	assert.Equal(t, "now", s.Subtitles.ProbeHash)
	assert.EqualValues(t, 6, s.Subtitles.Items[0].Attempts.Count, "the search history is kept")
	assert.Equal(t, "now", s.Markers.ForProbeHash)
	assert.Equal(t, "now", s.Markers.Analysis.ForProbeHash)
	assert.False(t, s.CarryBytes(), "nothing left to carry")

	for _, phase := range []TranscodePhase{TranscodePhaseQueued, TranscodePhaseRunning, TranscodePhaseSwapping} {
		s = moved()
		s.Transcode.Phase = phase
		s.CarryBytes()
		assert.Equal(t, "old", s.Transcode.ProbeHash, "a transcode %s names the old path in its task", phase)
	}
	for _, phase := range []GraftPhase{GraftPhaseQueued, GraftPhaseRunning, GraftPhaseSwapping} {
		s = moved()
		s.Graft.Phase = phase
		s.CarryBytes()
		assert.Equal(t, "old", s.Graft.ProbeHash, "a graft %s", phase)
	}
	s = moved()
	s.Subtitles.Items[0].Dispatch = &Dispatch{Seq: 4, AnsweredSeq: 3}
	s.CarryBytes()
	assert.Equal(t, "old", s.Subtitles.ProbeHash, "a fetch in flight names the old path")
	assert.Equal(t, "now", s.Markers.ForProbeHash, "the other blocks are carried all the same")

	// Blocks about other bytes: a hash the file never had.
	s = moved()
	s.Transcode.ProbeHash, s.Subtitles.ProbeHash, s.Markers.ForProbeHash = "other", "other", "other"
	s.Graft, s.Markers.Analysis = nil, nil
	assert.False(t, s.CarryBytes())
	assert.Equal(t, "other", s.Subtitles.ProbeHash)

	// No former hash, nothing carried; and the explicit form the file loop
	// uses for a status that does not hold the new hash yet.
	s = moved()
	s.FormerProbeHashes = nil
	assert.False(t, s.CarryBytes())
	s = moved()
	s.ProbeHash, s.FormerProbeHashes = "old", nil
	require.True(t, s.CarryBytesTo("now", []string{"old"}))
	assert.Equal(t, "now", s.Subtitles.ProbeHash)
	assert.Equal(t, "old", s.ProbeHash, "the probe hash is the probe planner's to move")
	var none *MediaFileStatus
	assert.False(t, none.CarryBytes())
}
