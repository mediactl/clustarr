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
	// applyRetry is a command the engine cannot apply yet: the payload RPC
	// failed, the disk is full.
	applyRetry = 30 * time.Second
	// notReadyRetry is a command that arrived before the re-attach report.
	notReadyRetry = 10 * time.Second
)

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
	case err != nil:
		log.WarnContext(ctx, "engine: cannot apply the command yet", "entry", cmd.Entry.ID, "seq", cmd.Seq, "error", err)
		return events.Retry(applyRetry, err)
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
