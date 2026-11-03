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
// person's pick is kept.
func (d *decider) supersede(entries []catalogv1alpha1.DownloadEntry) {
	for i := range entries {
		p := &entries[i]
		if !p.Pack() || p.Purpose != "" || !nonTerminal(p) {
			continue
		}
		covers := make(map[catalogv1alpha1.EpisodeNumber]bool, len(p.Episodes))
		for _, n := range p.Episodes {
			covers[n] = true
		}
		for j := range entries {
			s := &entries[j]
			if j == i || s.Purpose != "" || s.Manual || len(s.Episodes) != 1 || !covers[s.Episodes[0]] ||
				!transferring(s) || s.GrabbedAt.After(p.GrabbedAt.Time) {
				continue
			}
			s.RemoveDataOnDelete = true
			d.toRemoving(s, "superseded by the season pack "+p.Release.Title)
			d.event(EventNormal, ReasonSupersededByPack, "removing "+s.Release.Title+": the season pack "+p.Release.Title+" replaces it")
		}
	}
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
