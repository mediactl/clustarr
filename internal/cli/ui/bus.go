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

package uicli

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/internal/cli"
	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/metadata/extended"
	"github.com/mediactl/clustarr/pkg/obs"
	"github.com/mediactl/clustarr/ui"
)

// buildBus connects a bus under ui's own service name and returns the
// artwork object store ui/art.go's handleArt serves every image from --
// events.ObjectStore bound to events.BucketArtwork -- and the metadata
// search Add New asks, bound here to events.RPCMetadataSearch so the ui
// never holds a requester (2026-09-29), and the Plex provider's extras,
// bound to events.RPCMetadataExtras.
//
// ui never imports pkg/k8s (ui/guard_test.go bans it): this is why the bus
// is connected and bound here, in cmd/ui's command tree, and only the
// resulting handles cross into ui.Options. It does not ensure the topology:
// ui creates nothing and writes nothing here -- the manager is the only
// process that creates the streams and buckets (§5.9) -- and
// bus.ObjectStore's binding is lazy and cached (natsbus's own doc comment),
// so this never makes a network round trip just to hand back a handle.
//
// A connect failure is logged and swallowed, exactly like buildCluster's
// unreachable-Kubernetes case: Options.Artwork == nil is legal (ui/art.go
// answers 404 for every request), so a developer running `ui` with no NATS
// endpoint reachable still gets a working UI, only with placeholder art.
//
// An unreachable endpoint is NOT a connect failure: busconn.Connect sets
// nats.RetryOnFailedConnect, so it hands back a connection still
// reconnecting in the background, and every object-store read through it
// blocks until its own deadline. That is how a ui Deployment the installers
// gave no NATS_URL dialled the binary's default Service forever with nothing
// in its log (M7 final review). So the connection's state is logged once
// here, at startup, naming the URL it is trying.
//
// The returned close drains the connection; the command defers it around
// run, so the connection is drained when ui shuts down rather than left to
// the process exit. It is never nil.
func buildBus(ctx context.Context, natsURL string) uiBus {
	logger := log.FromContext(ctx).WithName("ui")
	bus, nc, err := busconn.Connect(natsURL, "ui", busconn.WithHooks(obs.BusHooks()))
	if err != nil {
		logger.Error(err, "connect ui to NATS; ui will serve placeholder art and no metadata search")
		return uiBus{close: func() {}}
	}
	if !nc.IsConnected() {
		logger.Info("NATS is not connected yet; /art, every Plex image and Add New's search will fail until it is -- "+
			"check --nats-url or $"+cli.EnvNATSURL, "url", natsURL, "status", nc.Status().String())
	}
	search := func(ctx context.Context, req schema.MetadataRequest) (schema.MetadataResponse, error) {
		var resp schema.MetadataResponse
		err := bus.Request(ctx, events.RPCMetadataSearch, req, &resp)
		return resp, err
	}
	// The extended-metadata read is a closure over Get alone: ui is handed
	// no KV handle it could write through (ui/guard_test.go).
	kv := bus.KV(events.BucketMetadataExtended)
	extendedRead := func(ctx context.Context, kind commonv1.MediaKind, uid types.UID) (extended.Doc, bool, error) {
		e, err := kv.Get(ctx, extended.Key(kind, uid))
		if errors.Is(err, events.ErrKeyNotFound) {
			return extended.Doc{}, false, nil
		}
		if err != nil {
			return extended.Doc{}, false, err
		}
		d, err := extended.Decode(e.Value)
		return d, err == nil, err
	}
	// The Plex provider's extras are the metadata gateway's to fetch and
	// store (clustarr-plex-extras): ui only asks, and holds no Plex token.
	extras := func(ctx context.Context, plexID string) ([]json.RawMessage, error) {
		ctx, cancel := context.WithTimeout(ctx, plexExtrasTimeout)
		defer cancel()
		var resp schema.PlexExtrasResponse
		if err := bus.Request(ctx, events.RPCMetadataExtras, schema.PlexExtrasRequest{PlexID: plexID}, &resp); err != nil {
			return nil, err
		}
		if resp.Error != "" {
			return nil, errors.New(resp.Error)
		}
		return resp.Extras, nil
	}
	return uiBus{
		artwork:  bus.ObjectStore(events.BucketArtwork),
		search:   search,
		extended: extendedRead,
		extras:   extras,
		close:    nc.Close,
	}
}

// plexExtrasTimeout bounds one extras request. A miss waits on the
// gateway's Plex rate limit while a library refresh asks for every item; a
// request that gives up answers PMS 502, which keeps the trailers it holds,
// and the item is fetched at a later refresh.
const plexExtrasTimeout = 30 * time.Second

// uiBus is what buildBus binds over ui's own bus connection: the artwork
// store and three closures, each a read or one request, so ui never holds
// a handle it could write through or ask anything else with.
type uiBus struct {
	artwork  events.ObjectStore
	search   ui.MetadataSearch
	extended func(ctx context.Context, kind commonv1.MediaKind, uid types.UID) (extended.Doc, bool, error)
	// extras asks the metadata gateway for a Plex id's extras
	// (rpc.catalogarr.metadata.extras); an answer carrying an error is one.
	extras func(ctx context.Context, plexID string) ([]json.RawMessage, error)
	// close drains the connection; never nil.
	close func()
}
