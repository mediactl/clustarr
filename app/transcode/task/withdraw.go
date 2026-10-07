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

package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// Withdrawal takes a MediaFile's transcode dispatch back (loop spec §5.10):
// one implementation for the remediation loop's Withdraw effect and the
// admission sweep, so the transcode manager need not import the loop's
// adapter. It imports only pkg/records and pkg/events.
type Withdrawal struct {
	Records *records.Requester[*Record]
	Leases  events.KV
	Admin   events.StreamAdmin
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Withdraw is §5.10's three steps for seq, in order: the record's
// compare-and-swap (one records.Withdraw, refusing to overwrite an
// unincorporated fact: records.ErrUnincorporatedFact, which the planner
// incorporates on its next pass), the lease's cancel marker
// (unconditional; a running worker reads it at its next renewal), then the
// purge of every task subject of the file -- a file has at most one live
// seq.
func (w Withdrawal) Withdraw(ctx context.Context, mfUID string, seq int64) error {
	if w.Records == nil || w.Leases == nil || w.Admin == nil {
		return errors.New("withdrawal failed: the transcode records, leases or stream admin are not wired")
	}
	key := events.RecordKey(mfUID)
	if err := w.withdrawRecord(ctx, key, seq); err != nil {
		return fmt.Errorf("withdrawal failed: %w", err)
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	cancel, err := json.Marshal(Lease{File: schema.Ref{UID: mfUID}, Seq: seq, State: LeaseCancelled, Since: now().UTC()})
	if err != nil {
		return fmt.Errorf("withdrawal failed: %w", err)
	}
	if _, err := w.Leases.Put(ctx, events.TranscodeLeaseKey(mfUID), cancel); err != nil {
		return fmt.Errorf("withdrawal failed: cancel the lease: %w", err)
	}
	if err := w.Admin.PurgeSubject(ctx, events.StreamWorkTranscode, events.WorkTranscodeTaskSubjectAnyPool(mfUID)); err != nil {
		return fmt.Errorf("withdrawal failed: purge the task: %w", err)
	}
	return nil
}

// withdrawRecord is the record's compare-and-swap, from a fresh read each
// attempt: the block incorporated everything before the seq it withdraws.
func (w Withdrawal) withdrawRecord(ctx context.Context, key string, seq int64) error {
	for range records.CASAttempts {
		_, rev, _, err := w.Records.Get(ctx, key)
		if err != nil {
			return err
		}
		_, err = w.Records.Withdraw(ctx, key, rev, seq, records.IncorporatedThrough(seq-1))
		if errors.Is(err, records.ErrRaced) {
			continue
		}
		return err
	}
	return records.ErrRaced
}

// WithdrawAll is Withdraw at math.MaxInt64, for a deleted file (§5.10): no
// task of it can run again, whatever seq it carries.
func (w Withdrawal) WithdrawAll(ctx context.Context, mfUID string) error {
	return w.Withdraw(ctx, mfUID, math.MaxInt64)
}
