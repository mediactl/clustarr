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

package wantedcron

import (
	"context"
	"fmt"
	"strings"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Schedule is when the sweep runs. It is deliberately the one-method subset of
// github.com/robfig/cron/v3's cron.Schedule, so a cron.Schedule from
// cron.ParseStandard satisfies it directly if a future task ever needs a
// user-configurable expression.
//
// This package does not import robfig/cron. §6.1 asks for one fixed twelve-
// hour sweep, TwelveHourly expresses that in eight lines, and a cron parser
// pulled in for a constant expression is a dependency nobody can account for.
// (The plan for this task assumed robfig/cron v3.0.1 was already in go.mod. It
// is not -- hack/deps/deps.go's comment claims it was pre-added but the blank
// import and the go.mod/go.sum entries are absent.)
type Schedule interface {
	// Next returns the next activation strictly after t.
	Next(t time.Time) time.Time
}

// TwelveHourly is the schedule §6.1 specifies: a sweep at 00:00 and 12:00
// every day, in the process's local time zone. It is exactly what
// cron.ParseStandard("0 */12 * * *") produces.
func TwelveHourly() Schedule { return everyNHoursOnTheHour(12) }

type everyNHoursOnTheHour int

func (n everyNHoursOnTheHour) Next(t time.Time) time.Time {
	step := int(n)
	// Truncate to the top of the hour in local time (time.Truncate works in
	// absolute time and would be wrong for a zone offset that is not a whole
	// number of hours), then advance to the next multiple of step.
	hour := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
	next := hour.Add(time.Duration(step-hour.Hour()%step) * time.Hour)
	for !next.After(t) {
		next = next.Add(time.Duration(step) * time.Hour)
	}
	return next
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;episodes;albums;books;audiobooks;issues,verbs=get;list;watch

// DonorBudget is the search planner's per-namespace budget of first-time
// live donor searches (searchplan.DonorBudget), which each sweep refills.
type DonorBudget interface {
	Refill(namespace string, n int)
}

// DonorSearchesPerSweep is the budget a sweep refills per namespace
// (searchplan.DonorSearchesPerSweep).
const DonorSearchesPerSweep = 20

// Runnable is the twelve-hourly missing/cutoff-unmet sweep. Since ADR-0019
// (§7.3, A4.2) it keeps only its timer: it decides nothing and publishes
// nothing, it wakes every due item's key in the remediation loop at low
// priority, where the item's search planner decides due, IndexOnly, the
// donor cap and pacing, and refills each namespace's donor budget. It holds
// no state between ticks: every sweep is a fresh List.
type Runnable struct {
	// Client is the manager's client. The sweep runs a List per tick rather
	// than a watch: it wants a point-in-time census twice a day, not a live
	// index, and the manager's cache serves it without an apiserver round
	// trip anyway.
	Client client.Client

	// Wakes receives every swept item's ref; the loop enqueues each at
	// handler.LowPriority. A send waits for room, so a sweep never drops an
	// item; nil wakes nothing.
	Wakes chan<- schema.ItemRef

	// Budget is refilled with DonorSearchesPerSweep per swept namespace.
	Budget DonorBudget

	// Schedule decides when to sweep. Nil means TwelveHourly.
	Schedule Schedule

	// Now is a seam for tests; nil means time.Now.
	Now func() time.Time

	// Namespaces limits the sweep. Nil sweeps cluster-wide, matching the
	// manager's own --watch-namespaces default.
	Namespaces []string

	// OnTick is a test-only hook called after each completed sweep.
	OnTick func(woken []string, err error)
}

// SetupWithManager registers the sweep.
func (r *Runnable) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(r)
}

// NeedLeaderElection makes the sweep singleton across replicas: the loop it
// wakes is the leader's.
func (r *Runnable) NeedLeaderElection() bool { return true }

func (r *Runnable) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runnable) schedule() Schedule {
	if r.Schedule != nil {
		return r.Schedule
	}
	return TwelveHourly()
}

// Start implements manager.Runnable. It returns nil on context cancellation:
// a Runnable that returns an error takes the whole manager down with it, and a
// graceful shutdown is not an error.
func (r *Runnable) Start(ctx context.Context) error {
	log := logging.FromContext(ctx).With("runnable", "wantedcron")
	sched := r.schedule()
	now := r.now()
	timer := time.NewTimer(sched.Next(now).Sub(now))
	defer timer.Stop()

	log.Info("wantedcron: started", "nextSweep", sched.Next(now))
	for {
		select {
		case <-ctx.Done():
			return nil
		case tick := <-timer.C:
			r.tick(ctx, tick)
			next := sched.Next(tick)
			timer.Reset(next.Sub(tick))
		}
	}
}

// tick runs one sweep with a panic guard. A panic in a Runnable is not caught
// by controller-runtime's RecoverPanic, which only wraps reconcilers, so it
// would otherwise crash the process every twelve hours.
func (r *Runnable) tick(ctx context.Context, at time.Time) {
	log := logging.FromContext(ctx).With("runnable", "wantedcron")
	defer func() {
		if rec := recover(); rec != nil {
			log.Error("wantedcron: sweep panicked", "panic", rec)
		}
	}()
	woken, err := r.runOnce(ctx, at)
	if err != nil {
		log.Error("wantedcron: sweep failed", "error", err)
	}
	if r.OnTick != nil {
		r.OnTick(woken, err)
	}
}

// runOnce is one sweep: list the catalog, refill each namespace's donor
// budget, and wake every item whose search or donor search is due by its
// backoff. It returns the namespaces it woke items in.
func (r *Runnable) runOnce(ctx context.Context, at time.Time) ([]string, error) {
	ctx, span := tracing.Start(ctx, "wantedcron.runOnce")
	defer span.End()

	var cands []Candidate
	for _, opts := range listOptions(r.Namespaces) {
		got, err := ListCandidates(ctx, r.Client, sweptKind, at, opts...)
		if err != nil {
			return nil, fmt.Errorf("wantedcron: %w", err)
		}
		cands = append(cands, got...)
	}

	namespaces := eligibleNamespaces(cands, at)
	if r.Budget != nil {
		for _, ns := range namespaces {
			r.Budget.Refill(ns, DonorSearchesPerSweep)
		}
	}
	woken := 0
	for _, c := range cands {
		if !c.Due(at, true) && !c.DonorDue(at) {
			continue
		}
		if r.Wakes == nil {
			break
		}
		ref := schema.ItemRef{Kind: kindName(c.Ref.Kind), Ref: schema.Ref{Namespace: c.Namespace, Name: c.Ref.Name, UID: c.UID}}
		select {
		case r.Wakes <- ref:
			woken++
		case <-ctx.Done():
			return namespaces, ctx.Err()
		}
	}
	logging.FromContext(ctx).Info("wantedcron: sweep complete",
		"items", len(cands), "woken", woken, "namespaces", len(namespaces))
	return namespaces, nil
}

// kindName is a media kind as its object Kind ("movie" reads "Movie").
func kindName(k commonv1.MediaKind) string {
	s := string(k)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// listOptions turns the watched-namespace set into List options: one
// cluster-wide List when unset, one List per namespace otherwise.
func listOptions(namespaces []string) [][]client.ListOption {
	if len(namespaces) == 0 {
		return [][]client.ListOption{nil}
	}
	out := make([][]client.ListOption, 0, len(namespaces))
	for _, ns := range namespaces {
		out = append(out, []client.ListOption{client.InNamespace(ns)})
	}
	return out
}

// sweptKind admits the kinds in SweptKinds.
func sweptKind(k commonv1.MediaKind) bool {
	for _, s := range SweptKinds {
		if s == k {
			return true
		}
	}
	return false
}
