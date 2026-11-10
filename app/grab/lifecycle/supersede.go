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

package lifecycle

import (
	"slices"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// supersede removes the single-episode grabs a live pack replaces (owner,
// 2026-11-03: "When a season pack is downloaded, we should *wait* to queue
// any episodes and remove any existing episode downloads for that season"):
// an automatic video entry of one episode, still transferring, grabbed no
// later than a live video pack of the owner covering its episode. Its data
// goes with it; nothing is blocklisted and no redownload is owed, since the
// pack brings the episode and holds it meanwhile
// (decision.ReasonPackDownloading). The grab planner grabs no pack beside a
// single that outranks it by its audio (grabplan.Holds), and the pack's
// files replace the singles' files at import (importplan's packRefit). A
// person's pick is kept. A Comic's volume pack supersedes its single-issue
// grabs the same way (2026-11-10 design D10: Issues get Episodes' pack
// rules), the single being an entry of exactly one issue the pack names.
func (d *decider) supersede(entries []catalogv1alpha1.DownloadEntry) {
	noun := "season pack"
	if d.v.Kind == commonv1.MediaKindComic {
		noun = "volume pack"
	}
	for i := range entries {
		p := &entries[i]
		if !p.Pack() || p.Purpose != "" || !nonTerminal(p) {
			continue
		}
		for j := range entries {
			s := &entries[j]
			if j == i || s.Purpose != "" || s.Manual || !coversSingle(p, s) ||
				!transferring(s) || s.GrabbedAt.After(p.GrabbedAt.Time) {
				continue
			}
			s.RemoveDataOnDelete = true
			d.toRemoving(s, "superseded by the "+noun+" "+p.Release.Title)
			d.event(EventNormal, ReasonSupersededByPack, "removing "+s.Release.Title+": the "+noun+" "+p.Release.Title+" replaces it")
		}
	}
}

// coversSingle reports s a single -- an entry of exactly one episode or one
// issue -- whose one item the pack p names.
func coversSingle(p, s *catalogv1alpha1.DownloadEntry) bool {
	switch {
	case len(s.Episodes) == 1 && len(s.Issues) == 0:
		return slices.Contains(p.Episodes, s.Episodes[0])
	case len(s.Issues) == 1 && len(s.Episodes) == 0:
		return slices.Contains(p.Issues, s.Issues[0])
	}
	return false
}

// transferring reports an entry whose payload is not yet complete.
func transferring(e *catalogv1alpha1.DownloadEntry) bool {
	switch e.Phase {
	case "", commonv1.DownloadPhasePending, commonv1.DownloadPhaseAssigned, commonv1.DownloadPhaseQueued,
		commonv1.DownloadPhaseDownloading, commonv1.DownloadPhasePaused:
		return true
	}
	return false
}
