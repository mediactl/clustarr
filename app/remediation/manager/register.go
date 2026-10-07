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

// Package manager is the remediation loop's manager-side registration (split
// spec §4.2.1's contract; loop spec §3.1): the loop, its indexes, its
// planners and actuators, under the manager's one lease.
package manager

import (
	"context"
	"errors"
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/probe"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/probestore"
	"github.com/mediactl/clustarr/pkg/records"
)

// Options is what the loop's registration takes.
type Options struct {
	k8s.Options
	DataDir             string
	Concurrency         int // --remediation-concurrency
	BulkWritesPerSecond int // --remediation-bulk-writes-per-second
	IOWorkers           int // --remediation-io-workers
}

// Deps is what Planners and Actuators build from.
type Deps struct {
	Env     *remediation.Env
	Options Options
}

// Register adds the loop, its indexes and its sources.
func Register(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if bus == nil {
		return errors.New("remediation manager: Register needs the bus")
	}
	if err := remediation.RegisterIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("remediation: indexes: %w", err)
	}
	env := &remediation.Env{
		Reader: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Bus: bus,
		IO:    remediation.NewIOExecutor(max(o.IOWorkers, 1)),
		Pacer: records.NewPacer(records.DefaultRates(), time.Now), DataDir: o.DataDir,
	}
	d := Deps{Env: env, Options: o}
	r, err := remediation.NewReconciler(mgr.GetClient(), env, mgr.GetEventRecorder(remediation.ControllerName),
		remediation.Config{Concurrency: o.Concurrency, BulkWritesPerSecond: o.BulkWritesPerSecond},
		Planners(d), Actuators(mgr, d)...)
	if err != nil {
		return err
	}
	return r.SetupWithManager(mgr)
}

// Planners is every planner the loop binds; the loop sorts them by Order.
// F3.3 and F3.4 add the naming and markers planners.
func Planners(d Deps) []remediation.Bound {
	probes := probestore.New(d.Env.Bus, probestore.WithErrors(func(op string) {
		metrics.RecordErrorsTotal.WithLabelValues("probe", op).Inc()
	}))
	return []remediation.Bound{
		remediation.Bind[mediafile.ProbeInput](probe.New(probe.Options{Bus: d.Env.Bus, Probes: probes})),
	}
}

// Actuators is every actuator; F3.3 adds rename, F3.5 replay.
func Actuators(_ ctrl.Manager, _ Deps) []remediation.Actuator { return nil }
