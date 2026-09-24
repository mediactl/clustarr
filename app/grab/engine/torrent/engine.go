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

package torrent

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// StallTimeout resolves spec.torrent.stallTimeout against its CRD default
// for pkg/download/torrent.Config: nil -- a DownloadClient built in Go that
// never saw the apiserver's defaulting -- is
// downloadv1alpha1.DefaultStallTimeout, and "0s" (or a negative value) is
// zero, which disables stall detection.
func StallTimeout(spec *downloadv1alpha1.TorrentSpec) time.Duration {
	if spec == nil || spec.StallTimeout == nil {
		return downloadv1alpha1.DefaultStallTimeout
	}
	return max(spec.StallTimeout.Duration, 0)
}

// Engine owns one embedded [download.Client] and the re-attach-before-ready
// gate every caller of it (this package's own [Reconciler], and
// [Engine.HealthzCheck]) must honour. See doc.go's "Re-attach gates
// readiness" for why both gates matter independently.
type Engine struct {
	// Client is the wrapped download client, normally built by
	// pkg/download/torrent.New.
	Client download.Client

	// StateDir is where [saveDescriptor]/[loadDescriptors] persist and read
	// re-attach state -- "<DataDir>/torrents/.state" in production.
	StateDir string

	mu    sync.Mutex
	ready atomic.Bool
}

// ReAttachResult summarises one [Engine.ReAttach] pass.
type ReAttachResult struct {
	// Attached is how many persisted transfers were successfully re-added.
	Attached int
	// Failed is how many persisted descriptors could not be read or
	// re-added. A non-zero count does not fail ReAttach outright -- see its
	// doc comment -- but every failure is logged and returned so the caller
	// can decide whether to alert on it.
	Failed int
}

// ReAttach loads every descriptor under [Engine.StateDir] and re-adds it
// through [Engine.Client], then marks the engine ready. It must be called
// exactly once, synchronously, before the engine accepts any other work --
// see doc.go and app/grab/run.go:216 for why.
//
// A single corrupt or unreadable descriptor is logged and skipped rather than
// aborting the whole pass: one damaged sidecar must not strand every other
// transfer's re-attach behind it, and a torrent this engine cannot recover a
// descriptor for is exactly the case [download.ErrNotFound] exists to let the
// Download controller retry (R4's own context: the controller reads
// ErrNotFound as "this engine restarted and has not re-attached this
// transfer", a retry rather than a failure).
//
// [download.Client.Add] is idempotent on the returned id (D2-1's own
// contract), so calling it again for a transfer the client already somehow
// knows about -- unreachable within one process's lifetime today, since
// nothing else calls Add before ReAttach runs -- would still be safe.
func (e *Engine) ReAttach(ctx context.Context) (ReAttachResult, error) {
	log := logging.FromContext(ctx)

	descriptors, loadErrs := loadDescriptors(e.StateDir)
	for _, err := range loadErrs {
		log.ErrorContext(ctx, "torrent: re-attach: failed to load a descriptor", "error", err)
	}

	var res ReAttachResult
	res.Failed = len(loadErrs)
	for _, d := range descriptors {
		id, err := e.Client.Add(ctx, d.addRequest())
		if err != nil {
			res.Failed++
			log.ErrorContext(ctx, "torrent: re-attach: add failed", "id", d.ID, "name", d.Desc.Name, "error", err)
			continue
		}
		if id != d.ID {
			// Add computes id from the metainfo's own info hash, which must
			// equal the id this descriptor was filed under -- the state
			// directory is keyed by id specifically so a mismatch here means
			// the persisted files were tampered with or corrupted in a way
			// JSON decoding did not catch (e.g. swapped .torrent files).
			res.Failed++
			log.ErrorContext(ctx, "torrent: re-attach: resolved id does not match persisted id",
				"persisted", d.ID, "resolved", id, "name", d.Desc.Name)
			continue
		}
		res.Attached++
		log.InfoContext(ctx, "torrent: re-attached", "id", id, "name", d.Desc.Name)
	}

	e.mu.Lock()
	e.ready.Store(true)
	e.mu.Unlock()

	log.InfoContext(ctx, "torrent: re-attach complete", "attached", res.Attached, "failed", res.Failed)
	return res, nil
}

// Ready reports whether [Engine.ReAttach] has completed.
func (e *Engine) Ready() bool { return e.ready.Load() }

// HealthzCheck is a controller-runtime healthz.Checker (func(*http.Request)
// error) that fails until [Engine.Ready]. Wiring it into
// k8s.AddProbes(mgr, ...)'s readyz set is D2-8's job (app/grab/run.go, outside
// this task's directory -- see the task instructions), but the check itself
// lives here so that wiring is a one-line call once it happens.
func (e *Engine) HealthzCheck(_ *http.Request) error {
	if !e.Ready() {
		return fmt.Errorf("torrent: re-attach not complete")
	}
	return nil
}
