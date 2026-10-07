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

package rollup

import (
	"slices"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// ObserveGraft is what an item's status.audio reads of a graft (loop spec
// §2.6, §2.14), for the Movie and Episode keys alike: an open donor entry,
// the donor -- the newest of imported (the donors the downloads stage found
// this pass), else the one prev recorded, carried forward -- the releases
// its file's graft rejected, appended to prev's, and the file's own
// status.graft. mf is the item's file, nil when it has none.
func ObserveGraft(prev *catalogv1alpha1.AudioState, imported []catalogv1alpha1.AudioDonor, mf *catalogv1alpha1.MediaFile, donorOpen bool) GraftObservation {
	var (
		recorded *catalogv1alpha1.AudioDonor
		rejected []string
	)
	if prev != nil {
		recorded, rejected = prev.Donor, prev.RejectedReleases
	}
	obs := GraftObservation{DonorDownloading: donorOpen, Donor: Donor(imported, recorded)}
	if mf != nil {
		obs.File, obs.MultiItem = mf.Status.Graft, len(mf.Spec.MediaRef.Keys) > 0
	}
	obs.Rejected = AppendRejected(rejected, obs.File)
	return obs
}

// Donor is status.audio.donor (loop spec §2.6; ADR-0019 F7.1): the newest
// of imported -- the donors the item's imported audioDonor entries
// delivered, as the downloads stage read them off the entries and their
// execute records -- else prev, the donor the item recorded before,
// carried forward, so an entry dropped from status.downloads (or an execute
// record the bucket's TTL retired) loses nothing. A donor imported no later
// than prev never replaces it; at the same instant the greater entry id
// wins, so two passes agree.
func Donor(imported []catalogv1alpha1.AudioDonor, prev *catalogv1alpha1.AudioDonor) *catalogv1alpha1.AudioDonor {
	best := prev.DeepCopy()
	for i := range imported {
		d := &imported[i]
		if d.Path == "" {
			continue
		}
		if best != nil && !newerDonor(d, best) {
			continue
		}
		best = d.DeepCopy()
	}
	return best
}

// newerDonor orders two donors by import time, then entry id.
func newerDonor(a, b *catalogv1alpha1.AudioDonor) bool {
	if !a.ImportedAt.Equal(&b.ImportedAt) {
		return b.ImportedAt.Before(&a.ImportedAt)
	}
	return a.DownloadRef > b.DownloadRef
}

// AppendRejected is status.audio.rejectedReleases (loop spec §2.14): prev,
// plus the donor release of a graft that failed by the donor's fault, once;
// the oldest dropped past catalogv1alpha1.MaxRejectedReleases.
func AppendRejected(prev []string, b *catalogv1alpha1.GraftState) []string {
	out := slices.Clone(prev)
	if b == nil || b.Phase != catalogv1alpha1.GraftPhaseFailed || !b.DonorFault || b.DonorRelease == "" || slices.Contains(out, b.DonorRelease) {
		return out
	}
	out = append(out, b.DonorRelease)
	if n := len(out); n > catalogv1alpha1.MaxRejectedReleases {
		out = out[n-catalogv1alpha1.MaxRejectedReleases:]
	}
	return out
}
