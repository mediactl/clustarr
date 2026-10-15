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

package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/grab/engine"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

func applies(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, "apply") {
			out = append(out, c)
		}
	}
	return out
}

// TestACommandAtOrBelowTheJournalSeqIsDropped: the engine applies a command
// only above the seq its journal holds (§6.7); a republish or a stale command
// -- an absent one included -- is acked with the record showing the
// journal's seq, and touches nothing.
func TestACommandAtOrBelowTheJournalSeqIsDropped(t *testing.T) {
	ctx := context.Background()
	hs := newHarness(t, journaled(entryID, entryUID, 10))
	hs.start(t)

	for _, c := range []schema.EngineCommand{
		command(entryID, entryUID, 10, schema.EngineDesiredPresent),
		command(entryID, entryUID, 9, schema.EngineDesiredAbsent),
		command(entryID, entryUID, 3, schema.EngineDesiredAbsent),
	} {
		require.NoError(t, hs.h.Handle(ctx, delivery(t, c)), "seq %d is acked", c.Seq)
	}
	assert.Empty(t, applies(hs.src.Calls()), "nothing applied at or below the journal's seq")
	h, ok := hs.src.get(entryID)
	require.True(t, ok, "a stale absent command removes nothing")
	assert.Equal(t, int64(10), h.Journal.Seq)
	rec, ok := hs.transfer(t, entryUID)
	require.True(t, ok)
	assert.Equal(t, int64(10), rec.Seq)
	assert.Equal(t, schema.TransferStatePresent, rec.State)
	assert.True(t, rec.Claimed, "a command named the entry")

	require.NoError(t, hs.h.Handle(ctx, delivery(t, command(entryID, entryUID, 11, schema.EngineDesiredPresent))))
	assert.Equal(t, []string{"apply present seq=11 cur=" + entryID}, applies(hs.src.Calls()))
	rec, _ = hs.transfer(t, entryUID)
	assert.Equal(t, int64(11), rec.Seq)
}

// TestAPreJournalTransferIsMatchedByNameAndClaimed: a transfer whose journal
// names no entry (added before journals existed, or by an earlier release)
// is listed unidentified until a command for an entry of its name arrives;
// that command claims it -- the journal gains the entry's uid, claim and seq
// -- rather than adding a second transfer (A3.6, cold-turkey revision).
func TestAPreJournalTransferIsMatchedByNameAndClaimed(t *testing.T) {
	ctx := context.Background()
	pre := engine.Held{
		Name:  entryID,
		Item:  download.Item{ID: "infohash-pre", Name: entryID, Status: download.StatusDownloading, AddedAt: time.Now().Add(-48 * time.Hour)},
		Found: true,
	}
	other := engine.Held{
		Name:  "by-hand",
		Item:  download.Item{ID: "infohash-hand", Name: "by-hand", Status: download.StatusDownloading, AddedAt: time.Now().Add(-48 * time.Hour)},
		Found: true,
	}
	hs := newHarness(t, pre, other)
	hs.start(t)

	er := hs.engineRecord(t)
	assert.Equal(t, int32(2), er.UnidentifiedCount)
	_, ok := hs.transfer(t, entryUID)
	assert.False(t, ok, "a pre-journal transfer has no entry uid, so no transfer record")

	require.NoError(t, hs.h.Handle(ctx, delivery(t, command(entryID, entryUID, 20, schema.EngineDesiredPresent))))
	assert.Equal(t, []string{"apply present seq=20 cur=" + entryID}, applies(hs.src.Calls()), "matched by name, not added anew")
	h, ok := hs.src.get(entryID)
	require.True(t, ok)
	assert.Equal(t, "infohash-pre", h.Item.ID, "the same transfer")
	assert.Equal(t, entryUID, h.Journal.EntryUID)
	assert.Equal(t, int64(20), h.Journal.Seq)
	require.NotNil(t, h.Journal.Claim)
	assert.Equal(t, owner(), h.Journal.Claim.Owner)

	rec, ok := hs.transfer(t, entryUID)
	require.True(t, ok)
	assert.Equal(t, int64(20), rec.Seq)
	assert.True(t, rec.Claimed)
	assert.Equal(t, "infohash-pre", rec.DownloadID)

	require.NoError(t, hs.rep.Resync(ctx, 1))
	er = hs.engineRecord(t)
	assert.Equal(t, int32(1), er.UnidentifiedCount, "only the transfer no entry names stays unidentified")
	require.Len(t, er.Unidentified, 1)
	assert.Equal(t, "infohash-hand", er.Unidentified[0].DownloadID)

	// A command for an entry of another name does not claim it.
	require.NoError(t, hs.h.Handle(ctx, delivery(t, command("other-entry", "uid-other", 30, schema.EngineDesiredPresent))))
	assert.Contains(t, applies(hs.src.Calls()), "apply present seq=30 cur=")
	_, still := hs.src.get("by-hand")
	assert.True(t, still)
}

// TestTheEngineAcksOnlyAfterTheRecordShowsTheSeq: over a real (membus)
// engine durable, a command whose transfer record cannot be written is not
// acked -- it is redelivered -- and is acked once the record shows its seq.
func TestTheEngineAcksOnlyAfterTheRecordShowsTheSeq(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hs := newHarness(t, journaled(entryID, entryUID, 10))
	hs.start(t)

	t.Run("a direct call returns a retry while the record cannot be written", func(t *testing.T) {
		hs.transfers.setClosed(true)
		err := hs.h.Handle(ctx, delivery(t, command(entryID, entryUID, 11, schema.EngineDesiredPresent)))
		var retry *events.RetryError
		require.ErrorAs(t, err, &retry, "not acked")
		hs.transfers.setClosed(false)
	})

	spec := events.EngineConsumer("qbt", 0)
	require.NoError(t, hs.bus.EnsureConsumer(ctx, spec))
	stop, err := hs.bus.Subscribe(ctx, spec.Subscription(), hs.h.Handle)
	require.NoError(t, err)
	defer stop()

	hs.transfers.setClosed(true)
	cmd := command(entryID, entryUID, 12, schema.EngineDesiredPresent)
	name, data, err := schema.Encode(cmd)
	require.NoError(t, err)
	msgID := events.MsgIDForEngineCommand(entryUID, cmd.Seq)
	_, err = hs.bus.Publish(ctx, events.WorkEngineSubject("qbt", 0, entryUID),
		&events.Envelope{ID: msgID, Type: "download.EngineCommand", Schema: name, Data: data, Time: hs.clock.Now()},
		events.WithMsgID(msgID), events.WithExpectStream(events.StreamWorkEngine))
	require.NoError(t, err)

	// membus polls on its (fake) clock: step it in its poll interval until
	// the command is delivered, far short of the handler's 10 s nak delay.
	require.Eventually(t, func() bool {
		hs.clock.Advance(2 * time.Millisecond)
		return len(filter(hs.src.Calls(), "apply present seq=12")) >= 1
	}, 5*time.Second, time.Millisecond, "delivered and applied")
	state := func() events.ConsumerState {
		cs, err := hs.bus.ConsumerState(ctx, events.StreamWorkEngine, spec.Name)
		require.NoError(t, err)
		return cs
	}
	require.Eventuallyf(t, func() bool { return state().AckPending == 1 }, 5*time.Second, 5*time.Millisecond,
		"state %+v calls %v", state(), hs.src.Calls())
	assert.Len(t, filter(hs.src.Calls(), "apply present seq=12"), 1, "not redelivered before its nak delay")
	assert.Equal(t, uint64(1), state().AckPending, "applied but its record not written: not acked")
	rec, _ := hs.transfer(t, entryUID)
	assert.Less(t, rec.Seq, cmd.Seq)

	hs.transfers.setClosed(false)
	require.Eventually(t, func() bool {
		hs.clock.Advance(15 * time.Second) // past the handler's nak delay
		rec, ok := hs.transfer(t, entryUID)
		cs := state()
		return ok && rec.Seq == cmd.Seq && cs.AckPending == 0 && cs.Pending == 0
	}, 10*time.Second, 20*time.Millisecond, "acked once the record shows the seq")
}

// recordedSince is the record writes since mark, in order.
func recordedSince(hs *harness, mark int) []string {
	return append([]string(nil), (*hs.writes)[mark:]...)
}

// TestResyncRewritesEveryRecordThenReportsTheSeq: after the transfers bucket
// is recreated (every record gone), a resync command rewrites each
// transfer's record and only then reports its resyncSeq in the engine record
// (§6.7, "Resync") -- so the manager judges no entry missing early.
func TestResyncRewritesEveryRecordThenReportsTheSeq(t *testing.T) {
	ctx := context.Background()
	hs := newHarness(t, journaled("a-entry", "uid-a", 5), journaled("b-entry", "uid-b", 6))
	hs.start(t)

	kv := hs.bus.KV(events.BucketTransfers)
	for _, uid := range []string{"uid-a", "uid-b"} {
		require.NoError(t, kv.Delete(ctx, agentrecords.TransferKey(uid)))
		_, ok := hs.transfer(t, uid)
		require.False(t, ok)
	}

	mark := len(*hs.writes)
	resync := schema.EngineCommand{Desired: schema.EngineDesiredResync, ResyncSeq: 42, Owner: dcRef(), IssuedAt: time.Now()}
	require.NoError(t, hs.h.Handle(ctx, delivery(t, resync)))

	for uid, seq := range map[string]int64{"uid-a": 5, "uid-b": 6} {
		rec, ok := hs.transfer(t, uid)
		require.True(t, ok, "%s rewritten", uid)
		assert.Equal(t, seq, rec.Seq)
		assert.Equal(t, "boot-1", rec.BootID)
	}
	assert.Equal(t, int64(42), hs.engineRecord(t).ResyncSeq)

	w := recordedSince(hs, mark)
	require.NotEmpty(t, w)
	last := w[len(w)-1]
	assert.True(t, strings.HasPrefix(last, "engines "), "the engine record is written last: %v", w)
	for _, x := range w[:len(w)-1] {
		assert.True(t, strings.HasPrefix(x, "transfers "), "only transfer records before it: %v", w)
	}
	assert.Len(t, w, 3)

	require.NoError(t, hs.h.Handle(ctx, delivery(t, schema.EngineCommand{Desired: schema.EngineDesiredResync, ResyncSeq: 7})))
	assert.Equal(t, int64(42), hs.engineRecord(t).ResyncSeq, "an older resync never lowers the reported seq")

	t.Run("a transfer record that cannot be written reports no resync", func(t *testing.T) {
		hs.transfers.setClosed(true)
		defer hs.transfers.setClosed(false)
		err := hs.h.Handle(ctx, delivery(t, schema.EngineCommand{Desired: schema.EngineDesiredResync, ResyncSeq: 50}))
		var retry *events.RetryError
		require.ErrorAs(t, err, &retry)
		assert.Equal(t, int64(42), hs.engineRecord(t).ResyncSeq)
	})
}

// TestAnUnclaimedTransferReadsClaimedFalseAfterTheGrace: a transfer no
// command named since the boot reads claimed: false only UnclaimedGrace
// after the boot (or its own add, when later), and claimed again once named.
func TestAnUnclaimedTransferReadsClaimedFalseAfterTheGrace(t *testing.T) {
	ctx := context.Background()
	hs := newHarness(t)
	boot := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	now := boot
	hs.rep.Now = func() time.Time { return now }

	old := journaled("old-entry", "uid-old", 4)
	old.Item.AddedAt = boot.Add(-time.Hour)
	young := journaled("young-entry", "uid-young", 4)
	young.Item.AddedAt = boot.Add(5 * time.Minute)

	claimed := func(h engine.Held) bool {
		require.NoError(t, hs.rep.WriteHeld(ctx, h, true))
		rec, ok := hs.transfer(t, h.Journal.EntryUID)
		require.True(t, ok)
		return rec.Claimed
	}
	assert.True(t, claimed(old), "at the boot: the reporter's first write dates its boot")
	now = boot.Add(time.Minute)
	assert.True(t, claimed(old), "inside the boot's grace")
	now = boot.Add(engine.UnclaimedGrace - time.Second)
	assert.True(t, claimed(old))
	now = boot.Add(engine.UnclaimedGrace + time.Second)
	assert.False(t, claimed(old), "past the grace, no command named it")
	assert.True(t, claimed(young), "a transfer added after the boot has its own grace")
	now = young.Item.AddedAt.Add(engine.UnclaimedGrace + time.Second)
	assert.False(t, claimed(young))

	hs.rep.Named("uid-old")
	assert.True(t, claimed(old), "named by a command since the boot")
	assert.Equal(t, engine.UnclaimedGrace, 10*time.Minute, "lifecycle.UnclaimedGrace's value")
}

// TestTransferRecordFilesAreClampedToTheBudget: a pack's file list is cut to
// schema.MaxTransferFilesBytes of JSON with filesTruncated, and its message
// to the record's bound, so one huge transfer still fits the bucket's value
// cap and is reported.
func TestTransferRecordFilesAreClampedToTheBudget(t *testing.T) {
	ctx := context.Background()
	hs := newHarness(t)

	big := journaled(entryID, entryUID, 3)
	for i := range 20_000 {
		big.Item.Files = append(big.Item.Files, download.File{
			Path:      fmt.Sprintf("Season %02d/%s.S%02dE%03d.1080p.WEB-DL.DDP5.1.H.264-GRP.mkv", i/100, strings.Repeat("Long.Show.Name.", 3), i/100, i%100),
			SizeBytes: int64(i) << 20,
		})
	}
	big.Item.Message = strings.Repeat("par2: ", 2000)
	require.NoError(t, hs.rep.WriteHeld(ctx, big, true), "the record fits the bucket's value cap")
	rec, ok := hs.transfer(t, entryUID)
	require.True(t, ok)
	assert.True(t, rec.FilesTruncated)
	assert.NotEmpty(t, rec.Files)
	assert.Less(t, len(rec.Files), len(big.Item.Files))
	b, err := json.Marshal(rec.Files)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(b), schema.MaxTransferFilesBytes)
	assert.Equal(t, big.Item.Files[len(rec.Files)-1].Path, rec.Files[len(rec.Files)-1].Path, "a prefix of the list, in order")
	assert.LessOrEqual(t, len([]rune(rec.Message)), schema.MaxTransferMessage)
	whole, err := json.Marshal(rec)
	require.NoError(t, err)
	assert.Less(t, len(whole), int(events.TransfersMaxValueSize))

	small := journaled("small-entry", "uid-small", 3)
	small.Item.Files = []download.File{{Path: "movie.mkv", SizeBytes: 1 << 30}, {Path: "sample.mkv", Skipped: true}}
	require.NoError(t, hs.rep.WriteHeld(ctx, small, true))
	rec, _ = hs.transfer(t, "uid-small")
	assert.False(t, rec.FilesTruncated)
	assert.Equal(t, []schema.TransferFile{{Path: "movie.mkv", SizeBytes: 1 << 30}, {Path: "sample.mkv", Skipped: true}}, rec.Files)
}

// TestDownloadTimeoutReReadsBeforeAskingAgain (ADR-0020 N4): a payload RPC
// that timed out is not asked again at once -- rpc.index.download counts a
// grab at the indexer and is not idempotent -- but after a longer delay, and
// the next delivery re-reads the engine's transfers before it applies, so a
// transfer that arrived meanwhile is updated rather than fetched again.
func TestDownloadTimeoutReReadsBeforeAskingAgain(t *testing.T) {
	ctx := context.Background()
	hs := newHarness(t)
	hs.start(t)
	cmd := command(entryID, entryUID, 40, schema.EngineDesiredPresent)

	hs.src.mu.Lock()
	hs.src.applyErr = fmt.Errorf("engine: payload: %w", context.DeadlineExceeded)
	hs.src.calls = nil
	hs.src.mu.Unlock()
	err := hs.h.Handle(ctx, delivery(t, cmd))
	var timeoutRetry *events.RetryError
	require.ErrorAs(t, err, &timeoutRetry)
	require.Equal(t, events.RequestTimeout, events.ClassifyRequestError(err))

	hs.src.mu.Lock()
	hs.src.applyErr = errors.New("engine: the disk is full")
	hs.src.mu.Unlock()
	err = hs.h.Handle(ctx, delivery(t, cmd))
	var otherRetry *events.RetryError
	require.ErrorAs(t, err, &otherRetry)
	assert.Greater(t, timeoutRetry.After, otherRetry.After, "a timed-out payload RPC waits longer than an ordinary retry")

	// The transfer arrived meanwhile under its entry's name (the usenet
	// client's ByName dedupe): the next delivery finds it first.
	hs.src.add(engine.Held{Name: entryID, Item: download.Item{ID: "nzb-1", Name: entryID, Status: download.StatusQueued}, Found: true})
	require.NoError(t, hs.h.Handle(ctx, delivery(t, cmd)))
	calls := hs.src.Calls()
	assert.Equal(t, []string{
		"list", "apply present seq=40 cur=",
		"list", "apply present seq=40 cur=",
		"list", "apply present seq=40 cur=" + entryID,
	}, filter(calls, "list", "apply"), "every delivery re-reads before it applies")
	rec, ok := hs.transfer(t, entryUID)
	require.True(t, ok)
	assert.Equal(t, "nzb-1", rec.DownloadID, "the transfer that arrived, not a second one")
}

func filter(calls []string, prefixes ...string) []string {
	var out []string
	for _, c := range calls {
		for _, p := range prefixes {
			if strings.HasPrefix(c, p) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// TestACommandSupersededByOneAlreadySeenIsAcked: the manager republishes an
// owed command at a new seq every 50 minutes, so an entry whose add cannot
// be applied -- its indexer down -- holds several copies in the queue, each
// retried on its own (about six per nzb.life entry on talos, 2026-10-15). A
// copy older than one the engine has already seen for its entry is acked
// unapplied: the newer carries the entry's whole desired state.
func TestACommandSupersededByOneAlreadySeenIsAcked(t *testing.T) {
	ctx := context.Background()
	hs := newHarness(t)
	hs.start(t)
	hs.src.mu.Lock()
	hs.src.calls = nil
	hs.src.mu.Unlock()
	handle := func(id, uid string, seq int64) error {
		failNextApply(hs, errors.New("index: indexer returned HTTP 522"))
		return hs.h.Handle(ctx, delivery(t, command(id, uid, seq, schema.EngineDesiredPresent)))
	}

	var retry *events.RetryError
	require.ErrorAs(t, handle(entryID, entryUID, 50), &retry)
	require.NoError(t, handle(entryID, entryUID, 40), "an older copy is acked")
	require.ErrorAs(t, handle(entryID, entryUID, 50), &retry, "the newest is retried")
	require.ErrorAs(t, handle("other-entry", "other-uid", 40), &retry, "another entry's seq is its own")
	assert.Equal(t, []string{"apply present seq=50 cur=", "apply present seq=50 cur=", "apply present seq=40 cur="},
		applies(hs.src.Calls()))
}

// TestAnApplyRetryGrowsWithTheAttempt: a command the engine cannot apply --
// its indexer down for hours -- is retried less often each time, 30 s
// doubling to an hour, so a long outage's retries, which the broker serves
// before new work, cannot fill the engine's slots (talos, 2026-10-15: 111
// retrying commands kept 1,153 undelivered).
func TestAnApplyRetryGrowsWithTheAttempt(t *testing.T) {
	ctx := context.Background()
	hs := newHarness(t)
	hs.start(t)
	cmd := command(entryID, entryUID, 60, schema.EngineDesiredPresent)
	for _, tc := range []struct {
		attempt uint64
		want    time.Duration
	}{{1, 30 * time.Second}, {2, time.Minute}, {3, 2 * time.Minute}, {4, 4 * time.Minute}, {7, 32 * time.Minute}, {8, time.Hour}, {20, time.Hour}} {
		failNextApply(hs, errors.New("index: indexer returned HTTP 522"))
		m := delivery(t, cmd)
		m.attempt = tc.attempt
		var retry *events.RetryError
		require.ErrorAs(t, hs.h.Handle(ctx, m), &retry)
		assert.Equal(t, tc.want, retry.After, "attempt %d", tc.attempt)
	}
}

// failNextApply makes the fake client's next Apply fail with err.
func failNextApply(hs *harness, err error) {
	hs.src.mu.Lock()
	hs.src.applyErr = err
	hs.src.mu.Unlock()
}
