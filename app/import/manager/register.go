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

// Package manager is importarr's manager-side registration (spec §4.2.1):
// the LibraryScan, RootFolder schedule, ImportExclusion, LibraryDelete and
// ImportList controllers and the recycle sweep's publisher. Completed
// downloads are imported by the downloads stage of the remediation loop
// (app/import/importplan, ADR-0019 §6.9); the download.clustarr.io/import
// intent replaced the retrigger controller. The streaming rename is the remediation loop's rename actuator
// (app/remediation/rename, ADR-0016).
package manager

import (
	"errors"
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/app/import/controller/importexclusion"
	importlistctrl "github.com/mediactl/clustarr/app/import/controller/importlist"
	"github.com/mediactl/clustarr/app/import/controller/librarydelete"
	"github.com/mediactl/clustarr/app/import/controller/libraryscan"
	"github.com/mediactl/clustarr/app/import/controller/recyclesweep"
	"github.com/mediactl/clustarr/app/import/controller/rootfolderschedule"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Options is what the import manager registration takes.
type Options struct {
	k8s.Options
	// TraktBaseURL points the ImportList controller's device-code flow
	// somewhere other than trakt.DefaultBaseURL (--trakt-base-url), the host
	// the list worker's syncs use, so a device authorization and the syncs it
	// authorizes name one host.
	TraktBaseURL string
}

// Register adds every importarr controller: importarr's leader-elected
// reconcilers (amendment §A1.2, §A1.3, §A1.6; §16 M1). Each call is the one
// its package's doc.go prescribes.
func Register(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if bus == nil {
		return errors.New("import manager: Register needs the bus")
	}

	if err := (&libraryscan.Reconciler{
		Client: mgr.GetClient(),
		Bus:    bus,
		Clock:  time.Now,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("importarr: libraryscan: %w", err)
	}

	// mgr.GetEventRecorder, not the deprecated mgr.GetEventRecorderFor: the
	// Recorder field is a k8s.io/client-go/tools/events.EventRecorder and
	// writes events.k8s.io/v1, which is what the package's RBAC marker
	// grants. See catalogarr's setupControllers for why the two must move
	// together.
	if err := (&rootfolderschedule.Reconciler{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorder("rootfolderschedule"),
		Clock:    time.Now,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("importarr: rootfolderschedule: %w", err)
	}

	if err := (&importexclusion.Reconciler{
		Client: mgr.GetClient(),
		Bus:    bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("importarr: importexclusion: %w", err)
	}

	// The library delete (docs/superpowers/specs/2026-09-30-library-delete-
	// design.md): carries out catalog.clustarr.io/delete on a library item,
	// removing its folder on disk for "files", so it runs here, where the
	// library is mounted, under the lease.
	if err := librarydelete.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("importarr: librarydelete: %w", err)
	}

	// The ImportList controller (plan task G1-3): schedules one
	// work.importarr.list.<name> task per spec.refreshInterval, drives
	// Trakt's device-code flow, and is the sole writer of ImportList.status,
	// which it projects from the list worker's clustarr-progress checkpoint.
	// HTTPClient and Clock are left nil on purpose: both default
	// (http.DefaultClient, bounded by the reconcile context; time.Now).
	if err := newImportListReconciler(mgr.GetClient(), bus, o).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("importarr: importlist: %w", err)
	}

	// The recycle-bin sweep's publisher (spec 2026-10-06 §3.5.3, OD36):
	// leader-only, one task per 6-hour slot on importarr-recycle.
	if err := mgr.Add(&recyclesweep.Scheduler{Bus: bus}); err != nil {
		return fmt.Errorf("import manager: add the recycle sweep scheduler: %w", err)
	}

	return nil
}

// newImportListReconciler builds the ImportList controller with o's Trakt
// base URL, the same host the import domain's list worker gets, so the
// device-code flow it drives authorizes the host the syncs then reach.
func newImportListReconciler(c client.Client, bus events.Bus, o Options) *importlistctrl.Reconciler {
	return &importlistctrl.Reconciler{Client: c, Bus: bus, TraktBaseURL: o.TraktBaseURL}
}
