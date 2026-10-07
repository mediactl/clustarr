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

// Package agent is the agent's index domain (spec §3.5.3, §5.12): the
// release index, the clustarr.rpc.indexarr.search/download/query verbs,
// indexarr-rss, the Torznab facade and its key, and the retention sweep.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	"github.com/mediactl/clustarr/app/indexer/clientcache"
	idxclients "github.com/mediactl/clustarr/app/indexer/clients"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/ratelimit"
	"github.com/mediactl/clustarr/pkg/relindex"
)

const (
	// DefaultIndexPath is the SQLite release index on the RWO PVC §6.2
	// mounts. It must equal the PVC's mountPath in
	// config/manager/indexarr.yaml: the pod runs with
	// readOnlyRootFilesystem, so anything outside that mount is unwritable.
	// This was "/index/releases.db", which no manifest ever used and which
	// the read-only root would have refused on the first write.
	DefaultIndexPath = "/var/lib/clustarr/index/releases.db"

	// DefaultFacadeBindAddress serves the Torznab facade §6.2 describes:
	// /{indexer}/api, /{indexer}/download and the aggregate /search/api.
	// It must equal the container port the Service routes to (8080, the
	// port named "http" in config/manager/indexarr.yaml and the chart);
	// TestDefaultFacadeBindAddressMatchesTheServicePort pins it. It was once
	// ":9696" -- Prowlarr's port -- which nothing routed to.
	DefaultFacadeBindAddress = ":8080"

	// DefaultFacadeAPIKeySecret is the Secret, in indexarr's own namespace,
	// whose data entries are the API keys the Torznab facade accepts. The
	// facade FAILS CLOSED -- facade.New refuses to build without a key --
	// so indexarr creates this Secret with one random key on first start
	// when it does not exist, the way Prowlarr generates its own API key.
	// config/ uses this name as-is; the chart's carries the release
	// fullname and reaches --facade-api-key-secret through
	// $CLUSTARR_FACADE_API_KEY_SECRET. See [ensureFacadeAPIKeys].
	DefaultFacadeAPIKeySecret = "indexarr-facade"

	// indexProbeTimeout bounds the readiness probe's Stats call so a wedged
	// SQLite handle fails readiness rather than hanging the probe handler.
	indexProbeTimeout = 5 * time.Second
)

// Options is what the index domain's Register takes.
type Options struct {
	k8s.Options
	IndexPath          string // SQLite release index; ignored when IndexDSN is set
	IndexDSN           string // Postgres DSN; selects relindex.OpenPostgres
	FacadeBindAddress  string // "0" disables the facade
	FacadeAPIKeySecret string // Secret in Namespace holding the facade's keys
	// Clients is the ClientCache the search fan-out, RSS poll and download
	// verb share. Nil builds one from clients.DefaultLimiterConfig with the
	// session store. Transitional: indexarr's --role all shim passes the cache
	// its Indexer reconciler evicts from; W4.25 (R8) deletes the field.
	Clients *clientcache.ClientCache
}

// Register opens the release index, registers the verbs, the RSS consumer,
// the sweep and (unless disabled) the facade, and returns the
// index.releaseindex check and the store's closer.
func Register(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
	if bus == nil {
		return catalogagent.Registration{}, errors.New("index domain: Register needs the bus")
	}
	if o.IndexDSN == "" && o.IndexPath == "" {
		return catalogagent.Registration{}, errors.New("index domain: an index path or a DSN is required")
	}
	facade := o.FacadeBindAddress != k8s.DisabledBindAddress
	if facade && (o.FacadeBindAddress == "" || o.FacadeAPIKeySecret == "" || o.Namespace == "") {
		return catalogagent.Registration{}, errors.New(
			"index domain: the Torznab facade needs a bind address, its API-key Secret and the namespace it lives in")
	}
	store, closer, err := openStore(ctx, o)
	if err != nil {
		return catalogagent.Registration{}, err
	}
	fail := func(err error) (catalogagent.Registration, error) {
		_ = closer.Close()
		return catalogagent.Registration{}, err
	}
	cc := o.Clients
	if cc == nil {
		// One Limiter for the whole domain, behind one ClientCache: the search
		// fan-out, the RSS poll and the download verb all pace against the
		// same bucket per host, and every wire client any of them uses --
		// Torznab or Cardigann -- comes out of the one builder, so
		// spec.proxyRef cannot reach one path and miss another.
		cc = clientcache.NewClientCache(mgr.GetClient(), ratelimit.New(idxclients.DefaultLimiterConfig()))
		// The KV half of the session store: without it the cache reads a
		// definition-backed Indexer's login session from the owned Secret
		// only, which is correct but a live apiserver GET per client build.
		cc.Sessions = idxclients.NewSessionStore(mgr.GetClient(), bus, k8s.ManagerIndexarrWorker)
	}
	v, err := registerWorkers(mgr, bus, store, cc)
	if err != nil {
		return fail(err)
	}
	if err := registerFacade(ctx, mgr, o, v); err != nil {
		return fail(err)
	}
	var ready k8s.Checks
	// §13's SQLite half. Stats is relindex's designated readiness call: it is
	// a real query against the handle, so it fails once the handle stops
	// working.
	if err := ready.Add("index.releaseindex", IndexReadyChecker(store)); err != nil {
		return fail(err)
	}
	return catalogagent.Registration{Ready: &ready, Close: closer.Close}, nil
}

// openStore opens the release index.
//
// The release index is opened BEFORE the manager starts, because the RSS
// worker, the search fan-out and the query verb are all constructed with
// it and a Store that appeared later would have to be reached through a
// nil check on every use. Open (or OpenPostgres) also creates and
// migrates the schema, so a success here is the process's proof that the
// index is open and writable -- the PVC under SQLite, the configured
// database under Postgres -- §13's "open and writable" half that no
// cheap periodic probe can restate without writing to the index every
// few seconds.
//
// A non-empty IndexDSN selects Postgres and IndexPath is ignored (spec
// §A.3); empty keeps SQLite, the default.
func openStore(ctx context.Context, o Options) (relindex.Store, io.Closer, error) {
	if o.IndexDSN != "" {
		logging.FromContext(ctx).Info("indexarr: release index on Postgres; --index-path ignored")
		store, closer, err := relindex.OpenPostgres(ctx, o.IndexDSN)
		if err != nil {
			return nil, nil, fmt.Errorf("indexarr: open the release index at the configured --index-dsn: %w", err)
		}
		return store, closer, nil
	}
	store, closer, err := relindex.Open(ctx, o.IndexPath)
	if err != nil {
		return nil, nil, fmt.Errorf("indexarr: open the release index at %s: %w", o.IndexPath, err)
	}
	return store, closer, nil
}

// IndexReadyChecker reports whether the release index is still usable, and is
// §13's "the SQLite index open and writable" readiness gate.
//
// It is a READ. The writable half is proved once, by [relindex.Open], which
// creates and migrates the schema before this checker is ever registered -- a
// process whose volume is read-only never reaches mgr.Start. Re-proving it on
// every probe would mean a write to the PVC every few seconds for the life of
// the pod, which buys a failure mode (the volume going read-only under a
// running pod) that no Clustarr deployment produces.
//
// Stats is relindex's own nominated probe: its doc says so, it runs a real
// query against the handle, and it deliberately tolerates a missing file when
// sizing the volume so a stat() race cannot flap readiness.
func IndexReadyChecker(store relindex.Store) healthz.Checker {
	return func(req *http.Request) error {
		if store == nil {
			return errors.New("release index: no store was opened")
		}
		ctx, cancel := context.WithTimeout(req.Context(), indexProbeTimeout)
		defer cancel()
		if _, err := store.Stats(ctx); err != nil {
			return fmt.Errorf("release index: %w", err)
		}
		return nil
	}
}
