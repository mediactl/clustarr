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

package naming

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// namingRetry is how soon a pass whose render kept its previous proposal
// over a failed lookup runs again (§3.11).
const namingRetry = 30 * time.Second

// Options configures the naming planner.
type Options struct{}

// Adapter is the naming planner.
type Adapter struct{}

// New is the naming planner.
func New(Options) *Adapter { return &Adapter{} }

// Input is what Gather read.
type Input struct {
	Lookup           mediafile.NamingLookup
	TranscodePending bool
	Read             bool // Gather ran
}

// Name implements remediation.Planner.
func (*Adapter) Name() remediation.PlannerName { return remediation.PlannerNaming }

// Applies: catalog names movies and episodes (mediafile's namingOwner).
func (*Adapter) Applies(mf *catalogv1alpha1.MediaFile) bool {
	k := mf.Spec.MediaRef.Kind
	return k == commonv1.MediaKindMovie || k == commonv1.MediaKindEpisode
}

// Gather reads the item and its RootFolder from the cache. The directory
// listing (status.sidecars) is the subtitles planner's since F5.1 (loop
// spec §6.4.3); whether a transcode holds the file is read off the draft in
// Plan (F6.2), since Gather runs before the transcode planner's Plan.
func (*Adapter) Gather(ctx context.Context, env *remediation.Env, v *remediation.View) (Input, error) {
	return Input{Read: true, Lookup: mediafile.LoadNaming(ctx, env.Reader, v.File)}, nil
}

// Plan renders status.naming and NamingCurrent against spec.path as of this
// pass's main apply.
func (*Adapter) Plan(v *remediation.View, in Input, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
	var res remediation.Result
	if !in.Read {
		return res, nil
	}
	// A file the probe planner found missing keeps the proposal it had:
	// nothing is rendered for a file that is not there (the MediaFile
	// controller's FileMissing return), and out already carries it.
	if fileMissing(v.Draft) {
		return res, nil
	}
	// A transcode in flight holds the rename: its worker re-probes the
	// source and swaps its output over it (spec D6).
	if t := v.Draft.Transcode; t != nil {
		switch t.Phase {
		case catalogv1alpha1.TranscodePhaseQueued, catalogv1alpha1.TranscodePhaseRunning, catalogv1alpha1.TranscodePhaseSwapping:
			in.TranscodePending = true
		}
	}
	n, retry := mediafile.RenderNaming(v.File, in.Lookup, v.Draft.MediaInfo, v.Prev.Naming, v.SpecPath(), probeFailed(v.Draft), in.TranscodePending)
	out.Naming = n
	if n == nil {
		k8s.RemoveCondition(&out.Conditions, catalogv1alpha1.ConditionNamingCurrent)
	} else {
		mediafile.MarkNamingCurrent(v.File, &out.Conditions, n)
	}
	if retry {
		res.Due = v.Now.Add(namingRetry)
	}
	return res, nil
}

// probeFailed is W4.13's probeStale for a failed probe of changed bytes: the
// draft's Probed reads ProbeFailed (D-F3-5).
func probeFailed(s *catalogv1alpha1.MediaFileStatus) bool {
	c := k8s.FindCondition(s.Conditions, catalogv1alpha1.MediaFileConditionProbed)
	return c != nil && c.Status == metav1.ConditionFalse && c.Reason == "ProbeFailed"
}

// fileMissing reports the probe planner's FileMissing on the draft: Ready
// False, reason FileMissing.
func fileMissing(s *catalogv1alpha1.MediaFileStatus) bool {
	c := k8s.FindCondition(s.Conditions, catalogv1alpha1.MediaFileConditionReady)
	return c != nil && c.Status == metav1.ConditionFalse && c.Reason == "FileMissing"
}

// Copy is naming's row of §3.5's table.
func (*Adapter) Copy(from, into *catalogv1alpha1.MediaFileStatus) {
	into.Naming = from.Naming.DeepCopy()
	remediation.CopyConditions(from, into, catalogv1alpha1.ConditionNamingCurrent)
}
