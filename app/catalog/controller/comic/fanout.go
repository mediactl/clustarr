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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
)

// SourceKey is the pkg/metadata ExternalIDs key a comic of this source is
// looked up by: ComicVine's volume id, or MangaDex's manga UUID. The
// metadata gateway keys a Comic's own fetch the same way
// (app/catalog/metadata/target.go) and its issue listing dispatches on the
// key it is handed (app/catalog/metadata/rpc.go's lookupIssues), so a
// MangaDex comic reaches only the providers that can read a MangaDex id.
func SourceKey(source catalogv1alpha1.ComicSourceProvider) string {
	if source == catalogv1alpha1.ComicSourceMangaDex {
		return extid.KeyMangaDex
	}
	return metadata.KeyComicVine
}

// DesiredIssue is one Issue object the Comic reconciler should ensure
// exists, with the provider-sourced fields to write to its status (under
// k8s.ManagerCatalogFanout) and the monitored decision (if any) to write
// to its spec, at Create only.
type DesiredIssue struct {
	Name                   string
	Number                 string
	CalculatedNumberCentis int32
	SourceID               string
	Title                  string
	Date                   *time.Time

	// The 2026-11-10 design's Issue fields (§4.7): the store date beside
	// the cover date, the issue's own synopsis, its cover's URL, the people
	// credited and the ids every provider holds for it.
	StoreDate *time.Time
	Overview  string
	CoverURL  string
	Credits   []metadata.Credit
	SourceIDs map[string]string

	// Monitored is non-nil only for an Issue object this pass is about to
	// create; nil means "do not touch spec.monitored" -- see DesiredIssues'
	// doc comment for the exact decision, and IssueSpec.Monitored's own doc
	// comment ("the Comic controller sets it at creation ... after that it
	// belongs to the user").
	Monitored *bool
}

// DesiredIssues projects a provider's issue list into the Issue objects the
// Comic reconciler should ensure exist for c. existingNames is the set of
// Issue object names already owned by c (from the reconciler's own List
// call); addOptionsApplied is c's own pre-reconcile status.addOptionsApplied,
// true once one full fan-out has applied spec.addOptions (2026-11-10 design
// D10; the Series' flag, which until then the IssuesSynced condition stood
// in for). It stays false across a partially-failed first pass (one issue
// created, the next Create failing transiently), so the add-time policy is
// re-applied to the issues that pass never reached rather than the
// "after add" one.
//
// Per issue, in order of precedence:
//  1. An Issue object already exists for this name: Monitored stays nil, the
//     user owns it from here on (IssueSpec.Monitored's own doc comment).
//  2. Number appears in spec.unmonitoredIssues: Monitored is set false,
//     regardless of add-time vs. later -- ComicSpec.UnmonitoredIssues' own
//     doc comment says this choice must survive an Issue object being
//     recreated, so it outranks both policies below unconditionally.
//  3. addOptionsApplied is false (this Comic has never completed a fan-out):
//     Monitored is spec.addOptions.monitor's answer for the issue
//     (InitialIssueMonitored: all, future, missing, existing, none; "all"
//     by default -- every issue known at add time is wanted, Kapowarr's
//     add-flow model, docs/research/quality.md §13).
//  4. Otherwise (a genuinely new issue discovered on a later refresh):
//     Monitored follows spec.monitorNewIssues (default true).
//
// Number is the provider's, or under specialVersion volumeAsIssue the
// volume it spells (VolumeIssueNumber), read before the name is made.
//
// SourceID is the issue's id under the comic's own source key (SourceKey):
// the ComicVine issue id for a ComicVine comic. A MangaDex comic's issues
// are MangaDex volumes, which have no id of their own, and an issue listed
// by a provider other than the comic's source (Metron answering for a
// ComicVine comic) carries that provider's id instead -- both leave SourceID
// empty rather than filing one provider's id as another's.
//
// Issues with a duplicate Number are deduplicated, first occurrence wins:
// ComicVine is assumed not to send duplicates, but this defends anyway, the
// same posture as series.DesiredEpisodes.
func DesiredIssues(
	c *catalogv1alpha1.Comic,
	addOptionsApplied bool,
	existingNames map[string]bool,
	issues []metadata.ComicIssue,
	now time.Time,
) []DesiredIssue {
	unmonitored := make(map[string]bool, len(c.Spec.UnmonitoredIssues))
	for _, n := range c.Spec.UnmonitoredIssues {
		unmonitored[n] = true
	}
	monitorNew := true
	if c.Spec.MonitorNewIssues != nil {
		monitorNew = *c.Spec.MonitorNewIssues
	}
	var runStatus catalogv1alpha1.ComicRunStatus
	if c.Status.Metadata != nil {
		runStatus = c.Status.Metadata.Status
	}

	sourceKey := SourceKey(c.Spec.Source)
	seen := make(map[string]bool, len(issues))
	out := make([]DesiredIssue, 0, len(issues))
	for _, iss := range issues {
		number := VolumeIssueNumber(c.Spec, iss.Number, iss.Title)
		if seen[number] {
			continue
		}
		seen[number] = true

		centis := CalculatedNumberCentis(number)
		name := IssueName(c.Name, number)

		var monitored *bool
		if !existingNames[name] {
			v := true
			switch {
			case unmonitored[number]:
				v = false
			case !addOptionsApplied:
				v = InitialIssueMonitored(c.Spec.AddOptions.Monitor,
					IssueCandidate{Number: number, Released: ReleaseDate(iss)}, runStatus, now)
			default:
				v = monitorNew
			}
			monitored = &v
		}

		date := iss.CoverDate
		if date == nil {
			date = iss.StoreDate
		}

		d := DesiredIssue{
			Name: name, Number: number, CalculatedNumberCentis: centis,
			SourceID: iss.IDs[sourceKey], Title: iss.Title, Date: date, Monitored: monitored,
			StoreDate: iss.StoreDate, Overview: iss.Description, Credits: iss.Credits,
		}
		if iss.Image != nil {
			d.CoverURL = iss.Image.URL
		}
		if len(iss.IDs) > 0 {
			d.SourceIDs = map[string]string(iss.IDs)
		}
		out = append(out, d)
	}
	return out
}
