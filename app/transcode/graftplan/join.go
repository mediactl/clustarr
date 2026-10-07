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

package graftplan

import (
	"slices"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/grafttask"
)

// JoinInput is what the transcode adapter hands Join: the same gather as
// the graft adapter's (graft.GatherItem).
type JoinInput struct {
	Item       *Item
	Root       string
	RecycleBin string
}

// Joinable is the graft a transcode dispatch carries (task.Task.Graft) and
// the join its block records (status.transcode.joinedGraft, §5.13, D42).
type Joinable struct {
	Task grafttask.Task
	Join catalogv1alpha1.TranscodeGraftJoin
}

// Join is the graft a transcode of the file would carry, over the file's
// own state (the AudioGraft controller's JoinTask, folded): the graft rides
// along when its block could exist, no standalone graft holds the file, no
// donor fault stands for this donor and these bytes, a language is missing
// and the file lies under a RootFolder. v.Draft is the transcode planner's
// draft; v.Issue is not used. nil when nothing rides along.
func Join(v View, in JoinInput) *Joinable {
	b := v.Prev.Graft
	if !exists(v, in.Item) || Standalone(b) || v.Draft.MediaInfo == nil || in.Root == "" {
		return nil
	}
	it := in.Item
	donor := it.Audio.Donor
	if b != nil && b.Phase == catalogv1alpha1.GraftPhaseFailed && b.DonorFault && sameAttempt(b, donor, v.Draft.ProbeHash) {
		return nil
	}
	l := FirstMissing(it.Audio.Missing, v.Draft.MediaInfo)
	if l == "" {
		return nil
	}
	return &Joinable{
		Task: grafttask.Task{
			Target: v.File.Spec.Path, TargetProbeHash: v.Draft.ProbeHash, Root: in.Root, Donor: donor.Path,
			Language: l, Languages: slices.Clone(it.Audio.Missing), Anchor: it.Anchor,
			Default: isDefault(it.Default, l), RecycleBin: in.RecycleBin,
		},
		Join: catalogv1alpha1.TranscodeGraftJoin{
			DonorRelease: donor.Release, DonorImportedAt: donor.ImportedAt.DeepCopy(),
			Languages: slices.Clone(it.Audio.Missing), ProbeHash: v.Draft.ProbeHash,
		},
	}
}

// SameJoin reports whether j is the join a block records: the same donor,
// languages and bytes. An owed republish carries the graft only then.
func SameJoin(j *Joinable, rec *catalogv1alpha1.TranscodeGraftJoin) bool {
	if j == nil || rec == nil {
		return false
	}
	a := j.Join
	return a.DonorRelease == rec.DonorRelease && a.ProbeHash == rec.ProbeHash && slices.Equal(a.Languages, rec.Languages) &&
		(a.DonorImportedAt == nil) == (rec.DonorImportedAt == nil) &&
		(a.DonorImportedAt == nil || a.DonorImportedAt.Equal(rec.DonorImportedAt))
}

// JoinPending reports the dispatch S whose joined graft the graft block has
// not incorporated -- the block is not at joinedTranscodeSeq S in a phase
// other than Running/JoinedTranscode: the transcode planner holds its next
// dispatch until it has (§5.13), so the graft planner can always still read
// that dispatch's record. A file with no graft block (its item stopped
// grafting, or lost its donor) holds nothing: no graft is left to read the
// record, and a hold would keep the transcode from ever dispatching again.
func JoinPending(tc *catalogv1alpha1.TranscodeState, b *catalogv1alpha1.GraftState) (int64, bool) {
	if tc == nil || tc.JoinedGraft == nil || tc.Dispatch == nil || b == nil {
		return 0, false
	}
	s := tc.Dispatch.Seq
	switch {
	case b.JoinedTranscodeSeq < s:
		return s, true
	case b.JoinedTranscodeSeq == s:
		return s, b.Phase == catalogv1alpha1.GraftPhaseRunning && b.Reason == ReasonJoinedTranscode
	}
	return s, false
}
