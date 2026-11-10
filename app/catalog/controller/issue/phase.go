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

package issue

import (
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/itemread"
)

// PhaseInputs is the Issue's view for rollup.LeafPhase, the one phase and
// cutoffReason the four non-video leaves share (2026-11-10 design D4; until
// then issue.State, Mylar's state enum). The facts the reconcile computed
// are passed in; the ones the Issue carries are read here:
//
//   - monitored is the Issue's own flag and its Comic's (itemread.Monitored).
//   - MetadataReady is always true: an Issue has no metadata document of
//     its own. Its provider fields (sourceID, title, date) are its Comic's,
//     written by the fan-out under catalog-fanout when it creates the Issue,
//     and an Issue is searched by its Comic and number whatever they say,
//     as Mylar searches an undated issue -- so an Issue never reads Pending.
//   - Released is whether the cover date has passed, an unknown date
//     counting as out (itemread.Available; Mylar decides undated ones);
//     an unreleased Issue with nothing on disk or in flight reads
//     Unavailable, the Episode's Unaired, where the retired Released
//     condition used to say it.
//   - CutoffEvaluated is whether the Comic's QualityProfile resolved; with
//     a file and no profile the Issue reads CutoffUnevaluated, never
//     CutoffUnmet, which would put it in the upgrade rotation on a verdict
//     that was never reached.
//   - PendingGrab is status.pendingGrab: a release chosen and waiting out a
//     DelayProfile delay, read-only here (the grab path writes it).
//
// A live grab entry overlays Downloading over the result (rollup.LeafOverlay),
// the caller's step, since the entry is the Comic's.
func PhaseInputs(iss *catalogv1alpha1.Issue, monitored, hasFile, cutoffMet, profileResolved bool, now time.Time) rollup.LeafInputs {
	return rollup.LeafInputs{
		Monitored:       monitored,
		MetadataReady:   true,
		Released:        itemread.Available(iss, now),
		HasFile:         hasFile,
		CutoffMet:       cutoffMet,
		CutoffEvaluated: profileResolved,
		PendingGrab:     iss.Status.PendingGrab != nil,
	}
}

// IdleInputs is the Issue's view for rollup.LeafIdleAtStart, the Issue
// watch's SkipInitial (design D14): the object as the cache lists it at a
// manager's start, with monitored read as the pass would read it. An Issue
// has no metadata document, so no refresh is ever due.
func IdleInputs(iss *catalogv1alpha1.Issue, monitored bool) rollup.LeafIdle {
	return rollup.LeafIdle{
		Generation:         iss.Generation,
		ObservedGeneration: iss.Status.ObservedGeneration,
		Monitored:          monitored,
		Phase:              iss.Status.Phase,
		ActiveDownloadRef:  iss.Status.ActiveDownloadRef,
		DownloadPhase:      iss.Status.DownloadPhase,
		PendingGrab:        iss.Status.PendingGrab != nil,
		SearchInFlight:     iss.Status.SearchDispatch.InFlight(),
		Conditions:         iss.Status.Conditions,
	}
}
