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

package remediationtest

import (
	"context"
	"sync/atomic"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
)

// Fake is a planner whose behaviour a test sets.
type Fake struct {
	N         remediation.PlannerName
	GatherFn  func(ctx context.Context, env *remediation.Env, v *remediation.View) (int, error)
	PlanFn    func(v *remediation.View, in int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error)
	CopyFn    func(from, into *catalogv1alpha1.MediaFileStatus)
	AppliesFn func(mf *catalogv1alpha1.MediaFile) bool
	// WithdrawFn runs a Withdraw effect the fake emitted; nil succeeds.
	WithdrawFn func(ctx context.Context, env *remediation.Env, w remediation.Withdraw) error
	Gathers    atomic.Int32
	Plans      atomic.Int32
	Withdraws  atomic.Int32
}

// Name implements remediation.Planner.
func (f *Fake) Name() remediation.PlannerName { return f.N }

// Applies implements remediation.Planner.
func (f *Fake) Applies(mf *catalogv1alpha1.MediaFile) bool {
	return f.AppliesFn == nil || f.AppliesFn(mf)
}

// Gather implements remediation.Planner.
func (f *Fake) Gather(ctx context.Context, env *remediation.Env, v *remediation.View) (int, error) {
	f.Gathers.Add(1)
	if f.GatherFn == nil {
		return 0, nil
	}
	return f.GatherFn(ctx, env, v)
}

// Plan implements remediation.Planner.
func (f *Fake) Plan(v *remediation.View, in int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
	f.Plans.Add(1)
	if f.PlanFn == nil {
		return remediation.Result{}, nil
	}
	return f.PlanFn(v, in, out)
}

// Copy implements remediation.Planner.
func (f *Fake) Copy(from, into *catalogv1alpha1.MediaFileStatus) { f.CopyFn(from, into) }

// Withdraw implements remediation.Withdrawer.
func (f *Fake) Withdraw(ctx context.Context, env *remediation.Env, w remediation.Withdraw) error {
	f.Withdraws.Add(1)
	if f.WithdrawFn == nil {
		return nil
	}
	return f.WithdrawFn(ctx, env, w)
}

// PlanFunc is a Fake's Plan.
type PlanFunc = func(*remediation.View, int, *catalogv1alpha1.MediaFileStatus) (remediation.Result, error)

// NamingFake owns status.naming and NamingCurrent: a real block, so a
// fake's apply is a real one.
func NamingFake(plan PlanFunc) *Fake {
	return &Fake{N: remediation.PlannerNaming, PlanFn: plan, CopyFn: func(from, into *catalogv1alpha1.MediaFileStatus) {
		into.Naming = from.Naming.DeepCopy()
		remediation.CopyConditions(from, into, catalogv1alpha1.ConditionNamingCurrent)
	}}
}

// MarkersFake owns status.markers.
func MarkersFake(plan PlanFunc) *Fake {
	return &Fake{N: remediation.PlannerMarkers, PlanFn: plan, CopyFn: func(from, into *catalogv1alpha1.MediaFileStatus) {
		into.Markers = from.Markers.DeepCopy()
	}}
}

// ProbeFake is named remediation.PlannerProbe and owns probeHash, probedAt,
// probeVersion, mediaInfo and the Probed and Ready conditions: the probe
// planner's fields, so the loop's structural gate applies to the planners
// after it.
func ProbeFake(plan PlanFunc) *Fake {
	return &Fake{N: remediation.PlannerProbe, PlanFn: plan, CopyFn: func(from, into *catalogv1alpha1.MediaFileStatus) {
		into.ProbeHash, into.ProbeVersion = from.ProbeHash, from.ProbeVersion
		into.ProbedAt = from.ProbedAt.DeepCopy()
		into.MediaInfo = from.MediaInfo.DeepCopy()
		remediation.CopyConditions(from, into, catalogv1alpha1.MediaFileConditionProbed, catalogv1alpha1.MediaFileConditionReady)
	}}
}

// CountingEffect counts its runs into n once its apply landed.
func CountingEffect(n *atomic.Int32) remediation.Effect {
	return remediation.Observation{Observe: func() { n.Add(1) }}
}

// PanickingEffect panics when it runs.
func PanickingEffect() remediation.Effect {
	return remediation.Observation{Observe: func() { panic("remediationtest: a panicking effect") }}
}
