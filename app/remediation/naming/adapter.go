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
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
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
type Adapter struct{ listings listings }

// New is the naming planner.
func New(Options) *Adapter { return &Adapter{} }

// Input is what Gather read.
type Input struct {
	Lookup           mediafile.NamingLookup
	TranscodePending bool
	Listing          *Listing // nil: neither listed nor remembered
	Read             bool     // Gather ran
}

// Name implements remediation.Planner.
func (*Adapter) Name() remediation.PlannerName { return remediation.PlannerNaming }

// Applies: catalogarr names movies and episodes (mediafile's namingOwner).
func (*Adapter) Applies(mf *catalogv1alpha1.MediaFile) bool {
	k := mf.Spec.MediaRef.Kind
	return k == commonv1.MediaKindMovie || k == commonv1.MediaKindEpisode
}

// Gather reads the item, its RootFolder and the file's TranscodeJobs from the
// cache, and lists the file's directory through the I/O executor when its
// listing is not remembered.
func (a *Adapter) Gather(ctx context.Context, env *remediation.Env, v *remediation.View) (Input, error) {
	in := Input{Read: true, Lookup: mediafile.LoadNaming(ctx, env.Reader, v.File)}
	jobs, err := mediafile.TranscodeJobsOf(ctx, env.Reader, v.File)
	if err != nil {
		return in, remediation.Transient(err)
	}
	in.TranscodePending = mediafile.TranscodeInFlight(jobs)
	need, remembered := a.listings.need(v.File)
	if !need {
		in.Listing = remembered
		return in, nil
	}
	dir := filepath.Dir(v.File.Spec.Path)
	entries, err := env.IO.ReadDir(ctx, dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		entries = nil
	case err != nil:
		return in, err // the executor marks a timeout, a busy pool and EIO Transient
	}
	li := Listing{Dir: dir}
	for _, e := range entries {
		if e.Type().IsRegular() {
			li.Names = append(li.Names, e.Name())
		}
	}
	a.listings.done(v.File, li)
	in.Listing = &li
	return in, nil
}

// Plan renders status.naming and NamingCurrent against spec.path as of this
// pass's main apply, and status.sidecars from the listing.
func (a *Adapter) Plan(v *remediation.View, in Input, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
	var res remediation.Result
	if !in.Read {
		return res, nil
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
	switch li := in.Listing; {
	case li == nil:
	case li.Dir == filepath.Dir(v.SpecPath()):
		out.Sidecars = sidecars(v.SpecPath(), *li)
	default:
		res.Again = true // a swap moved the file to another directory this pass: list it next pass
	}
	return res, nil
}

// probeFailed is W4.13's probeStale for a failed probe of changed bytes: the
// draft's Probed reads ProbeFailed (D-F3-5).
func probeFailed(s *catalogv1alpha1.MediaFileStatus) bool {
	c := k8s.FindCondition(s.Conditions, catalogv1alpha1.MediaFileConditionProbed)
	return c != nil && c.Status == metav1.ConditionFalse && c.Reason == "ProbeFailed"
}

// Copy is naming's row of §3.5's table, with sidecars until F5.1.
func (*Adapter) Copy(from, into *catalogv1alpha1.MediaFileStatus) {
	into.Naming = from.Naming.DeepCopy()
	into.Sidecars = slices.Clone(from.Sidecars)
	remediation.CopyConditions(from, into, catalogv1alpha1.ConditionNamingCurrent)
}
