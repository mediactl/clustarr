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
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
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

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;episodes,verbs=get;list;watch

// Runnable is the twelve-hourly missing/cutoff-unmet sweep. It holds no state
// between ticks: every sweep is a fresh List.
type Runnable struct {
	// Client is the manager's client. The sweep runs a List per tick rather
	// than a watch: it wants a point-in-time census twice a day, not a live
	// index, and the manager's cache serves it without an apiserver round
	// trip anyway.
	Client client.Client

	// Bus publishes the WantedScan messages.
	Bus events.Publisher

	// Schedule decides when to sweep. Nil means TwelveHourly.
	Schedule Schedule

	// Now is a seam for tests; nil means time.Now.
	Now func() time.Time

	// Namespaces limits the sweep. Nil sweeps cluster-wide, matching the
	// manager's own --watch-namespaces default.
	Namespaces []string

	// OnTick is a test-only hook called after each completed sweep.
	OnTick func(published []string, err error)
}

// SetupWithManager registers the sweep. This is the ONLY call
// catalogarr/run.go needs to make for wantedcron; see the package doc for the
// exact shape, including the fact that setupControllers does not yet receive
// an events.Bus.
func (r *Runnable) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(r)
}

// NeedLeaderElection makes the sweep singleton across replicas: §3 runs the
// controllers under a leader lease, and this is a controller-side job even
// though it reconciles nothing.
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
	published, err := r.runOnce(ctx, at)
	if err != nil {
		log.Error("wantedcron: sweep failed", "error", err)
	}
	if r.OnTick != nil {
		r.OnTick(published, err)
	}
}

// runOnce is one sweep: list the catalog, decide which namespaces still hold
// something worth searching for, and publish one low-tier WantedScan each.
//
// The Msg-Id is "wantedscan:<namespace>:<epoch>", so a re-fire inside the
// stream's deduplication window -- a leader flapping between replicas, say --
// does not enqueue a second sweep of the same namespace.
//
// A publish failure for one namespace does not abort the others: a full work
// queue in one namespace must not silence the whole cluster's sweep. The
// first error is returned once every namespace has been attempted.
func (r *Runnable) runOnce(ctx context.Context, at time.Time) ([]string, error) {
	ctx, span := tracing.Start(ctx, "wantedcron.runOnce")
	defer span.End()

	var movies []catalogv1alpha1.Movie
	var episodes []catalogv1alpha1.Episode
	for _, opts := range listOptions(r.Namespaces) {
		var ml catalogv1alpha1.MovieList
		if err := r.Client.List(ctx, &ml, opts...); err != nil {
			return nil, fmt.Errorf("wantedcron: list movies: %w", err)
		}
		movies = append(movies, ml.Items...)

		var el catalogv1alpha1.EpisodeList
		if err := r.Client.List(ctx, &el, opts...); err != nil {
			return nil, fmt.Errorf("wantedcron: list episodes: %w", err)
		}
		episodes = append(episodes, el.Items...)
	}

	namespaces := eligibleNamespaces(movies, episodes, at)
	epoch := at.Unix()
	published := make([]string, 0, len(namespaces))
	var firstErr error
	for _, ns := range namespaces {
		if err := r.publish(ctx, ns, epoch, at); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		published = append(published, ns)
	}
	logging.FromContext(ctx).Info("wantedcron: sweep complete",
		"movies", len(movies), "episodes", len(episodes), "namespaces", len(published))
	return published, firstErr
}

func (r *Runnable) publish(ctx context.Context, ns string, epoch int64, at time.Time) error {
	schemaName, data, err := schema.Encode(schema.WantedScan{
		Namespace:   ns,
		Kinds:       []commonv1.MediaKind{commonv1.MediaKindMovie, commonv1.MediaKindEpisode},
		CutoffUnmet: true,
		Epoch:       epoch,
	})
	if err != nil {
		return err
	}
	msgID := fmt.Sprintf("wantedscan:%s:%d", ns, epoch)
	env := &events.Envelope{
		ID:     msgID,
		Type:   "catalog.WantedScan",
		Schema: schemaName,
		Source: "catalogarr@" + version.String(),
		// The scan is namespace-wide, so there is no owning object to name.
		// The trailing slash keeps the key parseable by the <namespace>/<name>
		// convention every catalogarr consumer splits on.
		Key:  ns + "/",
		Time: at,
		Data: data,
	}
	if _, err := r.Bus.Publish(ctx, events.WorkWantedScanSubject(ns), env, events.WithMsgID(msgID)); err != nil {
		return fmt.Errorf("wantedcron: publish WantedScan for %q: %w", ns, err)
	}
	return nil
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
