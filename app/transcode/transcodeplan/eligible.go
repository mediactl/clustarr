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

package transcodeplan

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
)

// File is what Eligible reads of one file: the cached object, the view's
// labels, the draft status after the probe planner, and the cache facts the
// adapter gathered.
type File struct {
	MF              *catalogv1alpha1.MediaFile
	Labels          map[string]string
	Status          *catalogv1alpha1.MediaFileStatus
	ProbeCurrent    bool
	ItemExists      bool
	StandaloneGraft bool // graftplan.Standalone(status.graft): a standalone graft holds the file
	// Priority is the transcode.clustarr.io/priority intent when it is a
	// valid int32 (ParseIntents), nil when it is absent or invalid.
	Priority *int32
}

// Eligibility is §5.11's membership answer for one file.
type Eligibility struct {
	Eligible bool
	Profile  *transcodev1alpha1.TranscodeProfile // the winner, when there is one
	Hash     string                              // jobspec.ProfileHash(Profile.Spec)
	Tag      string                              // Tag(Profile.Name, Hash)
	Pending  string                              // ReasonUnprobed, ReasonProbePending or ReasonGrafting: eligible once it clears
	Why      string                              // for logs and Events when not eligible
	// Priority is the effective priority: the file's intent, else the
	// winning profile's spec.priority (admission orders by it).
	Priority int32
}

// Eligible is §5.11's membership rule over the one selection rule (§2.13):
// the ProfileLess-least selector match over the file's labels, else the
// default; an Invalid winner plans nothing; the item must exist; a
// transcoded file never plans again (CLAUDE.md, "Transcoding"); and a file
// whose probe is missing or pending, or which a standalone graft is
// rewriting, is eligible once that clears (Pending).
func Eligible(f File, profiles []transcodev1alpha1.TranscodeProfile) Eligibility {
	var e Eligibility
	if !EligibleKind(f.MF.Spec.MediaRef.Kind) {
		e.Why = "not a movie or episode"
		return e
	}
	labels := f.Labels
	if labels == nil {
		labels = f.MF.Labels
	}
	e.Profile = WinningProfile(labels, profiles, DefaultWinner(profiles))
	if e.Profile == nil {
		e.Why = "no TranscodeProfile selects it"
		return e
	}
	e.Hash = jobspec.ProfileHash(e.Profile.Spec)
	e.Tag = Tag(e.Profile.Name, e.Hash)
	e.Priority = e.Profile.Spec.Priority
	if f.Priority != nil {
		e.Priority = *f.Priority
	}
	view := f.MF.DeepCopy()
	if f.Status != nil {
		view.Status = *f.Status
	}
	switch invalid, _, msg := ValidateProfile(e.Profile, profiles); {
	case invalid:
		e.Why = "its winning profile is Invalid: " + msg
	case !f.ItemExists:
		e.Why = "its " + string(f.MF.Spec.MediaRef.Kind) + " is gone"
	case view.Transcoded() || AlreadyTranscoded(view, e.Profile.Name):
		e.Why = ReasonTranscoded
	case view.Status.ProbeHash == "" || view.Status.MediaInfo == nil:
		e.Why, e.Pending = "never probed", ReasonUnprobed
	case !f.ProbeCurrent:
		e.Why, e.Pending = "a probe of new bytes is pending", ReasonProbePending
	case f.StandaloneGraft:
		e.Why, e.Pending = "a standalone graft rewrites it", ReasonGrafting
	default:
		e.Eligible = true
	}
	return e
}
