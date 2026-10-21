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

// Package engine holds what grab's two transfer engines
// (app/grab/engine/torrent, app/grab/engine/usenet) share (ADR-0019 §6.7):
// the journal each keeps per transfer, the command handler that applies the
// manager's seq-fenced desired-state commands, the reporter that writes the
// transfer and engine records, and the 1 Hz progress publisher.
//
// # An engine executes, it never decides
//
// An engine reads no catalog object and writes no Kubernetes object. It
// reads its DownloadClient and its proxy or provider Secrets through the
// APIReader at start, and rolls when they change. Everything else arrives
// as an [schema.EngineCommand] on its own durable,
// grab-engine-<client>-<ordinal>, which the manager's DownloadClient
// controller creates: the transfer's whole desired state at a seq. The
// engine applies a command only above the seq its journal holds for the
// entry, acks only once the transfer record shows the new seq, and naks
// with a delay when it cannot apply yet.
//
// What the engine used to decide is the manager's now (§3.5 P111-P123): a
// stall, a removal after the import or the seed goal, the removal of a
// failed transfer, the orphan reaper. The engine removes a transfer, or any
// byte of data, only on an absent command at a seq above its journal's.
//
// # Claims and the journal
//
// Every transfer carries the claim it was added under -- its owner, the
// entry's id and uid, the release, the data rule -- in its journal and in
// its transfer record. A transfer no command has named since the engine's
// boot reads claimed: false once UnclaimedGrace passed; the manager then
// converges it to its owner's state. A transfer with no claim at all is
// listed in the engine record's unidentified.
package engine

import (
	"errors"
	"fmt"
	"time"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// ErrPayloadUnavailable is a payload the indexer or its link can no longer
// produce: a re-add answers the record failed with payloadUnavailable
// rather than retrying it forever (§6.7).
var ErrPayloadUnavailable = errors.New("engine: the payload can no longer be fetched")

// PayloadDeferredError is a payload the indexer will serve later, at Until:
// its grab limit's window or the pause a 429 it answered set
// (schema.DownloadResponse.RetryAt). The command is retried then, not on
// the generic doubling schedule, which spent an indexer's allowance as fast
// as it came back (nzb-life, 2026-10-16).
type PayloadDeferredError struct {
	Until time.Time
	Err   error
}

func (e *PayloadDeferredError) Error() string {
	return fmt.Sprintf("engine: the payload is deferred until %s: %v", e.Until.UTC().Format(time.RFC3339), e.Err)
}

func (e *PayloadDeferredError) Unwrap() error { return e.Err }

// MaxReleaseRefusals is how many times in a row the indexer may refuse one
// release (schema.DownloadResponse.ReleaseRefusals) before the engine gives
// it up: the first refusal is waited out; one more after its cooldown is
// the indexer saying it will not serve it (nzb-life refused three grabs
// every day for five days, 2026-10-15..20).
const MaxReleaseRefusals = 2

// ErrReleaseRefused is a release its indexer refused MaxReleaseRefusals
// times in a row: the command fails releaseRefused, which the manager does
// not blocklist.
var ErrReleaseRefused = errors.New("engine: the indexer keeps refusing this release")

// IndexerDeferred is a refusal resp names a RetryAt for, as the error an
// engine returns: ErrReleaseRefused once the release was refused
// MaxReleaseRefusals times in a row, else a PayloadDeferredError until
// resp.RetryAt.
func IndexerDeferred(resp schema.DownloadResponse, err error) error {
	if resp.ReleaseRefusals >= MaxReleaseRefusals {
		return fmt.Errorf("%w (%d refusals in a row): %w", ErrReleaseRefused, resp.ReleaseRefusals, err)
	}
	return PayloadDeferred(*resp.RetryAt, err)
}

// PayloadDeferred wraps err as a PayloadDeferredError until until.
func PayloadDeferred(until time.Time, err error) error {
	return &PayloadDeferredError{Until: until, Err: err}
}
