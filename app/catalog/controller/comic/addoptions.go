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

package comic

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// IssueCandidate is what InitialIssueMonitored reads of one provider issue:
// its number and the day it reached the shops (ReleaseDate).
type IssueCandidate struct {
	Number string
	// Released is the issue's store date, else its cover date; nil when the
	// provider dates it neither way.
	Released *time.Time
}

// ReleaseDate is the day an issue is out: its store date, the day it
// reached the shops, else its cover date -- a US comic's cover date runs
// about two months ahead of its store date, so the cover date alone would
// read an issue on the stands as still to come. Mylar's pull list keys on
// the store date (ReleaseDate) and falls back to the cover date (IssueDate)
// the same way (docs/research/quality.md §13).
func ReleaseDate(iss metadata.ComicIssue) *time.Time {
	if iss.StoreDate != nil {
		return iss.StoreDate
	}
	return iss.CoverDate
}

// InitialIssueMonitored decides an issue's monitored flag at add time, per
// the comic's ComicMonitorMode (2026-11-10 design D10: Mylar's add flow,
// where autowant_all is "all" and autowant_upcoming is "future", in Sonarr's
// vocabulary as the Series' SeriesMonitorMode spells it; series.
// InitialEpisodeMonitored is the model):
//
//	all       every issue the provider lists
//	future    only the issues not yet in shops, of a volume still running
//	missing   only the issues already out (at add time none has a file)
//	existing  none -- at add time no issue has a file yet (Sonarr's rule)
//	none      none
//
// An issue with no date at all counts as out for "missing" and not as to
// come for "future", the search worker's availability rule (an undated item
// is out). The default (an unset mode) is "all", the CRD's own default.
func InitialIssueMonitored(mode catalogv1alpha1.ComicMonitorMode, iss IssueCandidate, runStatus catalogv1alpha1.ComicRunStatus, now time.Time) bool {
	out := iss.Released == nil || !iss.Released.After(now)
	switch mode {
	case catalogv1alpha1.ComicMonitorAll, "":
		return true
	case catalogv1alpha1.ComicMonitorFuture:
		return !out && runStatus != catalogv1alpha1.ComicRunStatusEnded
	case catalogv1alpha1.ComicMonitorMissing:
		return out
	case catalogv1alpha1.ComicMonitorExisting, catalogv1alpha1.ComicMonitorNone:
		return false
	default:
		return false
	}
}

// NextPullDate is when the next issue of the comic is expected on the pull
// list (ComicStatus.nextPullDate, 2026-11-10 design D10): the earliest
// store date at or after now among its Issues -- each one's status.storeDate,
// else its cover date (status.date), as ReleaseDate reads a provider issue
// -- over every Issue, monitored or not, since the pull list is the
// volume's publication, not what the library wants of it. Nil when no issue
// is still to come.
func NextPullDate(issues []catalogv1alpha1.Issue, now time.Time) *metav1.Time {
	var next *metav1.Time
	for i := range issues {
		at := issues[i].Status.StoreDate
		if at == nil {
			at = issues[i].Status.Date
		}
		if at == nil || at.Time.Before(now) {
			continue
		}
		if next == nil || at.Time.Before(next.Time) {
			t := metav1.NewTime(at.Time)
			next = &t
		}
	}
	return next
}
