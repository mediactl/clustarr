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

// Package manager is indexarr's manager-side registration (spec §4.2.1,
// §5.12): the Indexer, IndexerDefinition, IndexerProxy and direct-grab
// reconcilers, and the Cardigann bundle loader behind the lease (R9).
package manager

import (
	"errors"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmanager "sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/mediactl/clustarr/app/indexer/bundle"
	"github.com/mediactl/clustarr/app/indexer/bundle/embedded"
	idxclients "github.com/mediactl/clustarr/app/indexer/clients"
	"github.com/mediactl/clustarr/app/indexer/controller/indexer"
	"github.com/mediactl/clustarr/app/indexer/controller/indexerdefinition"
	"github.com/mediactl/clustarr/app/indexer/controller/indexerproxy"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/ratelimit"
)

// Options is what the indexer manager registration takes.
type Options struct {
	k8s.Options
	// CardigannDefinitionsDir and CardigannBundled pick the bundle the loader
	// applies (--cardigann-definitions-dir, --cardigann-bundled).
	CardigannDefinitionsDir string
	CardigannBundled        bool
}

// Register adds indexarr's reconcilers (§6.2, §16 M2 and M6) -- Indexer,
// IndexerDefinition and IndexerProxy, plus the direct-grab counter that
// watches Downloads -- and the bundle loader. Each package's doc.go documents
// the exact call; these are those calls.
//
// All three take a k8s.io/client-go/tools/events.EventRecorder from
// mgr.GetEventRecorder, which writes events.k8s.io/v1 Events, and their
// +kubebuilder:rbac markers declare `groups=events.k8s.io` to match. The
// deprecated mgr.GetEventRecorderFor is not used anywhere in this tree;
// catalogarr's setupControllers carries the long note on why the marker and
// the recorder type have to move in the same commit.
//
// The Indexer reconciler paces on the manager's own limiter, which this
// function builds and nothing else shares: it is the only writer of that
// limiter's per-host Config. The index agent paces searches, RSS polls and
// grabs on its own ClientCache's limiter, in another process (R8, §5.12).
//
// bus is not optional, even though indexer.NewReconciler tolerates nil by
// logging a warning. The reconciler SEEDS the first RssTask (ruling R36) and
// the RSS worker schedules every one after it, so a nil bus means no chain
// ever starts and the release firehose publishes nothing -- which is the
// entire point of the worker. TestTheIndexerReconcilerGetsARealBus turns
// "someone notices a warning" into a failing test.
//
// The Cardigann login and the owned session Secret (plan task G1-1) live
// inside the Indexer reconciler, which NewReconciler wires to the bus's
// clustarr-indexer-sessions bucket. Proxy routing -- spec.proxyRef and every
// IndexerProxy whose spec.selector matches the Indexer -- is app/indexer/proxy's,
// applied by the one client builder every path shares and by the download
// fetcher, not by the IndexerProxy reconciler, which only probes reachability.
func Register(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if bus == nil {
		return errors.New("indexer manager: Register needs the bus")
	}
	c := mgr.GetClient()

	idxReconciler := indexer.NewReconciler(c, mgr.GetEventRecorder("indexer"),
		// The manager's own bucket per host: caps probes and logins only. The
		// index agent paces searches, polls and grabs on its own limiter (§5.12).
		ratelimit.New(idxclients.DefaultLimiterConfig()), bus)
	if err := idxReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("indexarr: indexer: %w", err)
	}

	if err := indexerdefinition.NewReconciler(
		c,
		mgr.GetEventRecorder("indexerdefinition"),
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("indexarr: indexerdefinition: %w", err)
	}

	// A grab whose source is a direct torrentURL/magnetURL/nzbURL never
	// reaches rpc.indexarr.download; the downloads stage counts it into the
	// same grab ring at the entry's Assigned edge (ADR-0019 §6.11), so the
	// directgrab controller is gone.

	// The nil *http.Client is indexerproxy.NewReconciler's documented
	// "http.DefaultClient". It is deliberate rather than an omission: the
	// prober bounds every probe with a context derived from
	// spec.requestTimeout, and an http.Client.Timeout here would be a hard
	// cap UNDER that, silently ignoring an operator who asked for longer.
	if err := indexerproxy.NewReconciler(
		c,
		mgr.GetEventRecorder("indexerproxy"),
		nil,
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("indexarr: indexerproxy: %w", err)
	}

	r, err := bundleRunnable(c, o)
	if err != nil {
		return err
	}
	if r != nil {
		if err := mgr.Add(r); err != nil {
			return fmt.Errorf("indexarr: add the Cardigann bundle loader: %w", err)
		}
	}
	return nil
}

// bundleRunnable is the Cardigann bundle loader as the manager adds it,
// leader-only (R9, §5.12). It reads from --cardigann-definitions-dir when
// set, else from the corpus embedded in cmd/manager when --cardigann-bundled
// is on. It is nil when neither asks for one.
//
// The loader applies every definition the bundle accepts as a labelled
// IndexerDefinition, once, after the caches sync, and leaves any same-named
// IndexerDefinition that is not the bundle's alone (see app/indexer/bundle).
// A bundle directory that cannot be read stops the manager: the operator
// asked for definitions that are not there.
//
// R9: a standby manager no longer re-applies all 752 definitions (about
// 1,500 API calls) on every pod start; under indexarr's SQLite --role all,
// which never elects, it still runs at once.
func bundleRunnable(c client.Client, o Options) (ctrlmanager.Runnable, error) {
	loader := &bundle.Loader{Client: c, Dir: o.CardigannDefinitionsDir}
	switch {
	case o.CardigannDefinitionsDir != "":
	case o.CardigannBundled:
		fsys, err := embedded.FS()
		if err != nil {
			return nil, fmt.Errorf("indexarr: %w", err)
		}
		loader.Dir, loader.FS = "(embedded)", fsys
	default:
		return nil, nil
	}
	return k8s.LeaderOnly(loader.Run), nil
}
