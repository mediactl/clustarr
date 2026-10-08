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
	"github.com/mediactl/clustarr/ui/projection"
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
// The returned close shuts the bus down through busconn.Shutdown; the command
// defers it around run, so the connection is drained when ui shuts down
// rather than left to the process exit. It is never nil.
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
		err := requestIdempotent(ctx, bus, events.RPCMetadataSearch, req, &resp)
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
		if err := requestIdempotent(ctx, bus, events.RPCMetadataExtras, schema.PlexExtrasRequest{PlexID: plexID}, &resp); err != nil {
			return nil, err
		}
		if resp.Error != "" {
			return nil, errors.New(resp.Error)
		}
		return resp.Extras, nil
	}
	// A grab's 1 Hz transfer telemetry and its import records: reads of
	// clustarr-progress and clustarr-imports, each under a short deadline,
	// closures over Get alone (ui/guard_test.go).
	progressKV := bus.KV(events.BucketProgress)
	progress := func(ctx context.Context, entryUID string) (schema.DownloadProgress, bool, error) {
		ctx, cancel := context.WithTimeout(ctx, busReadTimeout)
		defer cancel()
		e, err := progressKV.Get(ctx, transferProgressKey(entryUID))
		if errors.Is(err, events.ErrKeyNotFound) {
			return schema.DownloadProgress{}, false, nil
		}
		if err != nil {
			return schema.DownloadProgress{}, false, err
		}
		var p schema.DownloadProgress
		if err := json.Unmarshal(e.Value, &p); err != nil {
			return schema.DownloadProgress{}, false, err
		}
		return p, true, nil
	}
	// A running transcode's percent (loop spec §7.7): the transcode
	// worker's 1 Hz record, keyed "transcode.<mediafile-uid>" as
	// app/transcode/worker.ProgressKey builds it.
	transcode := func(ctx context.Context, uid types.UID) (int32, bool, error) {
		ctx, cancel := context.WithTimeout(ctx, busReadTimeout)
		defer cancel()
		e, err := progressKV.Get(ctx, "transcode."+events.KVKeyToken(string(uid)))
		if errors.Is(err, events.ErrKeyNotFound) {
			return 0, false, nil
		}
		if err != nil {
			return 0, false, err
		}
		var p schema.TranscodeProgress
		if err := json.Unmarshal(e.Value, &p); err != nil {
			return 0, false, err
		}
		return p.PercentMilli / 1000, true, nil
	}
	importsKV := bus.KV(events.BucketImports)
	importDetail := func(ctx context.Context, entryUID, phase string) (schema.ImportRecord, bool, error) {
		ctx, cancel := context.WithTimeout(ctx, busReadTimeout)
		defer cancel()
		e, err := importsKV.Get(ctx, events.RecordSubKey(entryUID, phase))
		if errors.Is(err, events.ErrKeyNotFound) {
			return schema.ImportRecord{}, false, nil
		}
		if err != nil {
			return schema.ImportRecord{}, false, err
		}
		var rec schema.ImportRecord
		if err := json.Unmarshal(e.Value, &rec); err != nil {
			return schema.ImportRecord{}, false, err
		}
		return rec, true, nil
	}
	return uiBus{
		artwork:   bus.ObjectStore(events.BucketArtwork),
		search:    search,
		extended:  extendedRead,
		extras:    extras,
		progress:  progress,
		transcode: transcode,
		imports:   importDetail,
		// busconn.Shutdown, every process's one shutdown path (ADR-0020
		// §1): ui serves no responder and binds no durable, so this drains
		// the connection -- its watches and outbound buffer -- and closes it.
		close: busconn.ShutdownAfter(ctx, busShutdownBudget, bus, nc),
	}
}

// transferProgressKey is a grab entry's transfer telemetry key in
// clustarr-progress, "download.<KVKeyToken(uid)>": app/grab/engine's
// ProgressKey, restated so cmd/ui links no app/ package and so neither
// pkg/k8s nor controller-runtime's manager (spec §4.5.2), the way the
// transcode closure restates its "transcode." key. A test pins the two
// together.
func transferProgressKey(entryUID string) string {
	return "download." + events.KVKeyToken(entryUID)
}

// idempotentRetries is the ui's own backoff for a busy metadata verb: a
// refusal is immediate, so a short wait inside the request's own deadline
// usually finds a slot.
var idempotentRetries = []time.Duration{250 * time.Millisecond, time.Second}

// requestIdempotent sends one of the metadata gateway's idempotent verbs
// (search, extras) and asks again on idempotentRetries while it is refused
// as busy and ctx allows (ADR-0020 §4). No responder fails fast -- no
// metadata agent is up, and waiting cannot change that inside one page
// load -- and a timeout has spent the request's deadline; both are named in
// the returned error.
func requestIdempotent(ctx context.Context, r events.Requester, subject string, in, out any) error {
	for attempt := 0; ; attempt++ {
		err := r.Request(ctx, subject, in, out)
		if events.ClassifyRequestError(err) != events.RequestBusy || attempt >= len(idempotentRetries) {
			return events.DescribeRequestError(subject, err)
		}
		t := time.NewTimer(idempotentRetries[attempt])
		select {
		case <-ctx.Done():
			t.Stop()
			return events.DescribeRequestError(subject, err)
		case <-t.C:
		}
	}
}

// busShutdownBudget bounds ui's bus shutdown once its context ends: well
// inside the Deployment's 30 s terminationGracePeriodSeconds.
const busShutdownBudget = 10 * time.Second

// busReadTimeout bounds one KV read the Downloads page makes.
const busReadTimeout = 2 * time.Second

// plexExtrasTimeout bounds one extras request. A miss waits on the
// gateway's Plex rate limit while a library refresh asks for every item; a
// request that gives up answers PMS 502, which keeps the trailers it holds,
// and the item is fetched at a later refresh.
const plexExtrasTimeout = 30 * time.Second

// uiBus is what buildBus binds over ui's own bus connection: the artwork
// store and closures, each a read or one request, so ui never holds
// a handle it could write through or ask anything else with.
type uiBus struct {
	artwork  events.ObjectStore
	search   ui.MetadataSearch
	extended func(ctx context.Context, kind commonv1.MediaKind, uid types.UID) (extended.Doc, bool, error)
	// extras asks the metadata gateway for a Plex id's extras
	// (rpc.catalog.metadata.extras); an answer carrying an error is one.
	extras func(ctx context.Context, plexID string) ([]json.RawMessage, error)
	// progress reads an entry's transfer telemetry; imports one of its
	// import records.
	progress func(ctx context.Context, entryUID string) (schema.DownloadProgress, bool, error)
	imports  func(ctx context.Context, entryUID, phase string) (schema.ImportRecord, bool, error)
	// transcode reads a MediaFile's running transcode percent from
	// clustarr-progress, for the pipeline projection.
	transcode projection.TranscodeProgressFunc
	// close drains the connection; never nil.
	close func()
}
