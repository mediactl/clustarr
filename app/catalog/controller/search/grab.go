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

package search

import (
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
)

// grabDecision is resolveGrab's verdict for one requested GUID.
type grabDecision struct {
	Release commonv1.ReleaseInfo
	Allowed bool
	Error   string
}

// resolveGrab looks guid up in results and applies spec §8.2's "Override
// required for Permanent rejections" rule: an approved release, or a rejected
// one whose Rejections are all temporary, grabs freely; a release carrying at
// least one Permanent rejection needs spec.override -- except
// decision.ReasonTranscodedFinal, which a person's own pick is exempt from:
// it bars automatic grabs over a transcoded file, and a spec.grab pick is
// the interactive grab that may replace one.
//
// A temporary rejection is deliberately not a barrier. Temporary means "this
// may pass on a later run" -- the queue already holds an equal candidate, the
// item is not available yet -- and a human looking at status.results and
// picking that release is making exactly the judgement call the temporary
// rejection was deferring.
//
// Since ADR-0019 (§6.6 step 4) an allowed pick is sent to the item's owner
// as a manual Candidate, and the owner's grab planner applies the same
// exemptions against its fresh state.
func resolveGrab(guid string, results []commonv1.ReleaseDecision, override bool) grabDecision {
	for _, r := range results {
		if r.GUID != guid {
			continue
		}
		if r.Approved {
			return grabDecision{Release: r.ReleaseInfo, Allowed: true}
		}
		for _, rej := range r.Rejections {
			// A transcoded file is final against every automatic grab, but a
			// person picking this release by hand is the interactive grab the
			// importer lets replace one (grabbedBy interactive, manual): the
			// one permanent rejection a pick needs no override for. A
			// spec.grabBest pick never reaches here unapproved.
			if decision.ReasonTranscodedFinal.Of(rej) {
				continue
			}
			// Likewise a full-season pack of a season still airing (Wave
			// S): the grab planner refuses it automatically and takes a
			// person's pick.
			if decision.ReasonFullSeasonNotAired.Of(rej) {
				continue
			}
			// And a release that would replace a good transcode baseline
			// (2026-10-16): an HDR file by an SDR release, an HEVC one by
			// AVC. A person picking it by hand means to.
			if decision.ReasonLosesHDR.Of(rej) || decision.ReasonAVCOverHEVC.Of(rej) {
				continue
			}
			if rej.Type == commonv1.RejectionPermanent && !override {
				return grabDecision{
					Release: r.ReleaseInfo,
					Error:   "release was permanently rejected: " + rej.Reason + "; set spec.override to grab it anyway",
				}
			}
		}
		return grabDecision{Release: r.ReleaseInfo, Allowed: true}
	}
	return grabDecision{Error: "guid " + guid + " is not in status.results"}
}
