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

package engine

import (
	"context"
	"errors"
	"sync"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Retry delays of the command handler (P116).
const (
	// applyRetry is a command the engine cannot apply yet -- the payload RPC
	// failed, the disk is full -- at its first attempt; applyRetryAfter
	// doubles it each attempt up to maxApplyRetry.
	applyRetry    = 30 * time.Second
	maxApplyRetry = time.Hour
	// notReadyRetry is a command that arrived before the re-attach report.
	notReadyRetry = 10 * time.Second
)

// applyRetryAfter is the retry of a command the engine could not apply at
// attempt: applyRetry doubling each attempt up to maxApplyRetry. The broker
// serves redeliveries before new work, so a long outage's retries at one
// rate -- nzb.life answering 522 for hours, each attempt holding a slot for
// its 15 s -- filled the slots and kept 1,153 commands, removals among
// them, undelivered (talos, 2026-10-15).
func applyRetryAfter(attempt uint64) time.Duration {
	d := applyRetry
	for i := uint64(1); i < attempt && d < maxApplyRetry; i++ {
		d *= 2
	}
	return min(d, maxApplyRetry)
}

// downloadTimeoutRetryAfter is the retry of a command whose payload RPC
// timed out: rpc.index.download is not idempotent (index fetches from the
// indexer and counts a grab), so the command waits twice an ordinary
// retry; the next delivery re-reads the transfers before it asks
// (ADR-0020 §4).
func downloadTimeoutRetryAfter(attempt uint64) time.Duration { return 2 * applyRetryAfter(attempt) }

// Handler applies the manager's engine commands (§6.7), bound by the
// engine's subscription to its own durable (the manager's DownloadClient
// controller created it; the engine only binds).
type Handler struct {
	Transfers Transfers
	Reporter  *Reporter
	// Engine is "<client>-<ordinal>".
	Engine string
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	// newest is the highest seq seen per entry uid (superseded).
	newest map[string]int64
}

// superseded reports whether a command for cmd's entry at a higher seq has
// been seen, and records cmd's seq otherwise. The manager republishes an owed
// command at a new seq every 50 minutes, so an entry whose add cannot be
// applied holds several copies in the queue, each retried on its own; the
// newest carries the entry's whole desired state, so an older one is acked
// unapplied. A restart forgets: the journal's seq check still drops a stale
// copy once one is applied.
func (h *Handler) superseded(cmd schema.EngineCommand) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.newest == nil {
		h.newest = map[string]int64{}
	}
	if cmd.Seq < h.newest[cmd.Entry.UID] {
		return true
	}
	h.newest[cmd.Entry.UID] = cmd.Seq
	return false
}

// lock serialises one entry's commands: a republish and its successor may
// arrive on two slots at once.
func (h *Handler) lock(uid string) func() {
	h.mu.Lock()
	if h.locks == nil {
		h.locks = map[string]*sync.Mutex{}
	}
	l, ok := h.locks[uid]
	if !ok {
		l = &sync.Mutex{}
		h.locks[uid] = l
	}
	h.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Handle applies one command:
//
//   - resync: rewrite every transfer record, then report the resyncSeq;
//   - downloadID: remove an unidentified transfer, its bytes kept;
//   - otherwise the entry's whole desired state at cmd.Seq, dropped when the
//     journal already holds that seq or a later one, matched to a
//     pre-journal transfer by name (which it then adopts), applied, and
//     acked only once the transfer record shows the seq.
func (h *Handler) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "engine.Handle")
	defer span.End()
	log := logging.FromContext(ctx).With("engine", h.Engine)

	var cmd schema.EngineCommand
	if err := schema.Decode(env.Schema, env.Data, &cmd); err != nil {
		return events.Discard("engine: malformed EngineCommand", err)
	}
	if !h.Reporter.Ready() {
		return events.Retry(notReadyRetry, errors.New("engine: the re-attach report is not complete"))
	}

	switch {
	case cmd.Desired == schema.EngineDesiredResync:
		if err := h.Reporter.Resync(ctx, cmd.ResyncSeq); err != nil {
			return events.Retry(notReadyRetry, err)
		}
		return nil
	case cmd.DownloadID != "":
		err := h.Transfers.RemoveByID(ctx, cmd.DownloadID, cmd.RemoveData)
		if err != nil && !errors.Is(err, download.ErrNotFound) {
			return events.Retry(applyRetry, err)
		}
		log.InfoContext(ctx, "engine: removed an unidentified transfer, its bytes kept", "downloadID", cmd.DownloadID)
		return nil
	case cmd.Entry.UID == "":
		return events.Discard("engine: a command names no entry", nil)
	case h.superseded(cmd):
		log.DebugContext(ctx, "engine: a newer command for the entry was seen; acking this one", "entry", cmd.Entry.ID, "seq", cmd.Seq)
		return nil
	}

	unlock := h.lock(cmd.Entry.UID)
	defer unlock()

	cur, err := h.find(ctx, cmd)
	if err != nil {
		return events.Retry(applyRetry, err)
	}
	h.Reporter.Named(cmd.Entry.UID)
	if cur != nil && cur.Journal.EntryUID == cmd.Entry.UID && cmd.Seq <= cur.Journal.Seq {
		// Already applied (a republish, a duplicate): the record shows it.
		if err := h.Reporter.WriteHeld(ctx, *cur, true); err != nil {
			return events.Retry(notReadyRetry, err)
		}
		return nil
	}
	if cur == nil && cmd.Desired == schema.EngineDesiredAbsent {
		if err := h.Reporter.WriteRemoved(ctx, cmd); err != nil {
			return events.Retry(notReadyRetry, err)
		}
		return nil
	}

	after, err := h.Transfers.Apply(ctx, cmd, cur)
	switch {
	case errors.Is(err, ErrPayloadUnavailable):
		log.WarnContext(ctx, "engine: the payload can no longer be fetched", "entry", cmd.Entry.ID, "error", err)
		if werr := h.Reporter.WriteFailed(ctx, cmd, string(commonv1.DownloadFailurePayloadUnavailable), err.Error()); werr != nil {
			return events.Retry(notReadyRetry, werr)
		}
		return nil
	case errors.Is(err, download.ErrPayloadMismatch):
		if werr := h.Reporter.WriteFailed(ctx, cmd, string(commonv1.DownloadFailurePayloadMismatch), err.Error()); werr != nil {
			return events.Retry(notReadyRetry, werr)
		}
		return nil
	case events.ClassifyRequestError(err) == events.RequestTimeout:
		// The payload RPC timed out: index may have fetched it and counted
		// the grab. Never asked again blindly -- the next delivery runs
		// find, and the usenet client's ByName, before it asks again.
		log.WarnContext(ctx, "engine: the payload RPC timed out; re-reading before asking again",
			"entry", cmd.Entry.ID, "seq", cmd.Seq, "error", err)
		return events.Retry(downloadTimeoutRetryAfter(m.Attempt()), err)
	case err != nil:
		log.WarnContext(ctx, "engine: cannot apply the command yet", "entry", cmd.Entry.ID, "seq", cmd.Seq,
			"attempt", m.Attempt(), "error", err)
		return events.Retry(applyRetryAfter(m.Attempt()), err)
	}
	if err := h.Reporter.WriteHeld(ctx, after, true); err != nil {
		return events.Retry(notReadyRetry, err)
	}
	return nil
}

// find is the transfer a command names: by its journal's entry uid, else a
// pre-journal transfer by name (the adopted entry's id is its Download's
// name, §10.2).
func (h *Handler) find(ctx context.Context, cmd schema.EngineCommand) (*Held, error) {
	held, err := h.Transfers.List(ctx)
	if err != nil {
		return nil, err
	}
	var byName *Held
	for i := range held {
		switch {
		case held[i].Journal.EntryUID == cmd.Entry.UID:
			return &held[i], nil
		case held[i].Journal.EntryUID == "" && held[i].Name == cmd.Entry.ID && byName == nil:
			byName = &held[i]
		}
	}
	return byName, nil
}

// NextJournal is the journal a transfer carries after cmd is applied.
func NextJournal(prev Journal, cmd schema.EngineCommand, now time.Time) Journal {
	claim := cmd.Claim
	j := prev
	j.EntryUID = cmd.Entry.UID
	j.Seq = cmd.Seq
	j.Claim = &claim
	j.Desired = cmd.Desired
	if cmd.Imported && !j.Imported {
		j.Imported = true
		at := now.UTC()
		if cmd.ImportedAt != nil {
			at = cmd.ImportedAt.UTC()
		}
		j.ImportedAt = &at
	}
	j.Claim.Imported = j.Imported
	if cmd.HealthOverride != "" {
		j.HealthOverride = cmd.HealthOverride
	}
	return j
}
