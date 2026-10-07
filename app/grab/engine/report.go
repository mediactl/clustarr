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
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/util/uuid"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// The reporter's cadence (§6.7).
const (
	// EngineRecordInterval is how often the engine record is rewritten.
	EngineRecordInterval = 60 * time.Second
	// CounterInterval bounds a transfer record's rewrites for counters
	// alone.
	CounterInterval = time.Minute
	// RefreshInterval is the longest a live transfer's record goes
	// unwritten, so the bucket's 7-day TTL retires only dead transfers.
	RefreshInterval = 24 * time.Hour
	// UnclaimedGrace is how long after the engine's boot, or a transfer's
	// add, claimed: false means nothing (lifecycle.UnclaimedGrace).
	UnclaimedGrace = 10 * time.Minute
)

// Reporter writes one engine's transfer records (clustarr-transfers, one
// per entry) and its engine record (clustarr-engines, one per instance),
// the engine's only report to the manager (§6.7). Each record has one
// writer: this engine pod.
type Reporter struct {
	Transfers *records.Writer[*schema.TransferRecord]
	Engines   *records.Writer[*schema.EngineRecord]
	// Source lists the engine's transfers.
	Source Transfers
	// Client is the DownloadClient (kind, namespace, name, uid).
	Client  schema.ItemRef
	Ordinal int32
	// Engine is "<client>-<ordinal>".
	Engine  string
	Pod     string
	BootID  string
	Version string
	// Proxy reports the proxy's UDP state: schema.ProxyUDP*; nil is n/a.
	Proxy func() string
	// Free reports the scratch and publish directories' free bytes.
	Free func() (scratch, publish int64)
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu          sync.Mutex
	bootAt      time.Time
	named       map[string]bool
	last        map[string]reported
	completedAt map[string]time.Time
	resyncSeq   int64
	reattached  bool
	ready       atomic.Bool
	engineSeq   int64
}

// reported is what the reporter last wrote for one entry.
type reported struct {
	fingerprint string
	counters    string
	at          time.Time
}

func (r *Reporter) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Reporter) init() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.named == nil {
		r.named = map[string]bool{}
		r.last = map[string]reported{}
		r.completedAt = map[string]time.Time{}
	}
	if r.BootID == "" {
		r.BootID = string(uuid.NewUUID())
	}
	if r.bootAt.IsZero() {
		r.bootAt = r.now()
	}
}

// NeedLeaderElection implements manager.LeaderElectionRunnable: every
// engine pod reports itself.
func (r *Reporter) NeedLeaderElection() bool { return false }

// Start implements manager.Runnable: the re-attach report -- every
// transfer's record at this boot, then the engine record with a new bootID
// and reattached -- then the engine record every EngineRecordInterval and
// on change, and transfer records on change, counters at most once a
// minute and at least once a day.
func (r *Reporter) Start(ctx context.Context) error {
	r.init()
	log := logging.FromContext(ctx).With("runnable", "engine-reporter", "engine", r.Engine)
	for {
		if err := r.writeAll(ctx, true); err != nil {
			log.WarnContext(ctx, "engine: the re-attach report failed; retrying", "error", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
				continue
			}
		}
		break
	}
	r.mu.Lock()
	r.reattached = true
	r.mu.Unlock()
	if err := r.writeEngine(ctx); err != nil {
		log.WarnContext(ctx, "engine: the engine record failed", "error", err)
	}
	r.ready.Store(true)
	log.InfoContext(ctx, "engine: re-attached and reported", "bootID", r.BootID)

	tick := time.NewTicker(time.Second * 10)
	defer tick.Stop()
	lastEngine := r.now()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if err := r.writeAll(ctx, false); err != nil {
				log.WarnContext(ctx, "engine: a transfer report failed", "error", err)
			}
			if r.now().Sub(lastEngine) >= EngineRecordInterval {
				if err := r.writeEngine(ctx); err != nil {
					log.WarnContext(ctx, "engine: the engine record failed", "error", err)
					continue
				}
				lastEngine = r.now()
			}
		}
	}
}

// Ready reports that every transfer record of this boot was written and
// the engine record says reattached.
func (r *Reporter) Ready() bool { return r.ready.Load() }

// HealthzCheck is a readiness check (healthz.Checker) that fails until
// Ready: torrent-engine.reattach and usenet-engine.reattach.
func (r *Reporter) HealthzCheck(_ *http.Request) error {
	if !r.Ready() {
		return fmt.Errorf("engine %s: re-attach report not complete", r.Engine)
	}
	return nil
}

// Named records that a command named the entry since this boot: its record
// reads claimed from now on.
func (r *Reporter) Named(entryUID string) {
	r.init()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.named[entryUID] = true
}

// Resync rewrites every transfer record, then reports seq in the engine
// record (§6.7, "Resync").
func (r *Reporter) Resync(ctx context.Context, seq int64) error {
	r.init()
	if err := r.writeAll(ctx, true); err != nil {
		return err
	}
	r.mu.Lock()
	if seq > r.resyncSeq {
		r.resyncSeq = seq
	}
	r.mu.Unlock()
	return r.writeEngine(ctx)
}

// writeAll writes each identified transfer's record when due (or always,
// with force).
func (r *Reporter) writeAll(ctx context.Context, force bool) error {
	held, err := r.Source.List(ctx)
	if err != nil {
		return fmt.Errorf("engine: list transfers: %w", err)
	}
	var errs []error
	for _, h := range held {
		if !h.Identified() {
			continue
		}
		if err := r.WriteHeld(ctx, h, force); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("engine: %d transfer records failed, first: %w", len(errs), errs[0])
	}
	return nil
}

// WriteHeld writes h's transfer record at its journal's seq: on any change
// of state, stage or claim, for counters at most once a minute, at least
// once a day, and always with force (a command, a resync, the boot).
func (r *Reporter) WriteHeld(ctx context.Context, h Held, force bool) error {
	r.init()
	if !h.Identified() {
		return nil
	}
	state := schema.TransferStatePresent
	switch {
	case !h.Found:
		state = schema.TransferStateRemoved
	case h.Item.Status == download.StatusFailed || h.Item.FailureReason.IsFailure():
		state = schema.TransferStateFailed
	}
	rec := r.record(h, state)
	fp, counters := fingerprint(rec)
	uid := h.Journal.EntryUID
	r.mu.Lock()
	prev, seen := r.last[uid]
	now := r.now()
	due := force || !seen || prev.fingerprint != fp ||
		(prev.counters != counters && now.Sub(prev.at) >= CounterInterval) ||
		now.Sub(prev.at) >= RefreshInterval
	r.mu.Unlock()
	if !due {
		return nil
	}
	if _, err := r.Transfers.Write(ctx, agentrecords.TransferKey(uid), rec); err != nil {
		return fmt.Errorf("engine: transfer record %s: %w", uid, err)
	}
	r.mu.Lock()
	r.last[uid] = reported{fingerprint: fp, counters: counters, at: now}
	if state == schema.TransferStateRemoved {
		delete(r.last, uid)
	}
	r.mu.Unlock()
	return nil
}

// WriteFailed writes a failed record for an entry the engine could not add
// (payloadUnavailable, payloadMismatch) at the command's seq.
func (r *Reporter) WriteFailed(ctx context.Context, cmd schema.EngineCommand, reason, message string) error {
	r.init()
	h := Held{
		Name: cmd.Entry.ID,
		Journal: Journal{
			EntryUID: cmd.Entry.UID, Seq: cmd.Seq, Claim: &cmd.Claim,
			FailureReason: reason, Desired: cmd.Desired,
		},
		Found: true,
	}
	h.Item.Status = download.StatusFailed
	h.Item.FailureReason = commonv1.DownloadFailureReason(reason)
	h.Item.Message = message
	h.Item.AddedAt = r.now()
	return r.WriteHeld(ctx, h, true)
}

// WriteRemoved writes a removed record for an entry the engine holds no
// transfer for, at the command's seq (an absent command with nothing to
// remove).
func (r *Reporter) WriteRemoved(ctx context.Context, cmd schema.EngineCommand) error {
	h := Held{
		Name:    cmd.Entry.ID,
		Journal: Journal{EntryUID: cmd.Entry.UID, Seq: cmd.Seq, Claim: &cmd.Claim, Desired: cmd.Desired},
	}
	return r.WriteHeld(ctx, h, true)
}

// record renders h's transfer record.
func (r *Reporter) record(h Held, state string) *schema.TransferRecord {
	it := h.Item
	claim := h.Journal.Claim
	owner := claim.Owner
	uid := h.Journal.EntryUID
	r.mu.Lock()
	claimed := r.named[uid] || r.now().Before(maxTime(r.bootAt, it.AddedAt).Add(UnclaimedGrace))
	if it.Status == download.StatusCompleted || it.Stage == "done" || it.Stage == "seeding" {
		if _, ok := r.completedAt[uid]; !ok {
			r.completedAt[uid] = r.now()
		}
	}
	completed, hasCompleted := r.completedAt[uid]
	r.mu.Unlock()

	rec := &schema.TransferRecord{
		RecordHeader: schema.RecordHeader{Item: &owner, Seq: h.Journal.Seq, State: state},
		Entry:        schema.EntryRef{ID: claim.Entry.ID, UID: uid},
		Claim:        claim,
		Claimed:      claimed,
		Engine:       r.Engine,
		BootID:       r.BootID,
		DownloadID:   it.ID,
		Stage:        it.Stage,
		OutputPath:   it.OutputPath,
		ContentRoot:  it.ContentRoot,
		TotalBytes:   it.TotalBytes, RemainingBytes: it.RemainingBytes,
		DownloadedBytes: it.DownloadedBytes, UploadedBytes: it.UploadedBytes,
		ProgressPercent: clampPercent(it.ProgressPercent), RatioMilli: max(it.RatioMilli, 0),
		SeedTimeSeconds: int64(it.SeedTime / time.Second),
		Seeders:         clampInt32(it.Seeders), Peers: clampInt32(it.Peers),
		IsEncrypted: it.IsEncrypted, CanMoveFiles: it.CanMoveFiles, CanBeRemoved: it.CanBeRemoved,
		SeedGoalReached: it.SeedGoalMet, HealthPaused: it.HealthPaused,
		EngineFailureReason: it.FailureReason,
		Message:             download.ClampMessage(it.Message),
		LastProgressAt:      it.LastProgressAt,
		AddedAt:             it.AddedAt,
		DataRemoved:         state == schema.TransferStateRemoved && h.Journal.Desired == schema.EngineDesiredAbsent,
	}
	if !it.AddedAt.IsZero() {
		started := it.AddedAt
		rec.StartedAt = &started
	}
	if hasCompleted {
		rec.CompletedAt = &completed
	}
	if hp := it.Health; hp != nil {
		rec.Health = &schema.TransferHealth{
			HealthPercent: hp.HealthPercent, CriticalHealthPercent: hp.CriticalHealthPercent,
			FailedArticles: hp.FailedArticles, TotalArticles: hp.TotalArticles,
		}
	}
	rec.Files, rec.FilesTruncated = clampFiles(it.Files)
	return rec
}

// writeEngine writes this instance's engine record.
func (r *Reporter) writeEngine(ctx context.Context) error {
	held, err := r.Source.List(ctx)
	if err != nil {
		return fmt.Errorf("engine: list transfers: %w", err)
	}
	now := r.now()
	r.mu.Lock()
	seq := max(r.engineSeq+1, now.UnixMilli())
	r.engineSeq = seq
	rec := &schema.EngineRecord{
		RecordHeader: schema.RecordHeader{Item: &r.Client, Sub: strconv.Itoa(int(r.Ordinal)), Seq: seq, State: schema.TransferStatePresent},
		Client:       r.Client.Name, Ordinal: r.Ordinal, Pod: r.Pod, BootID: r.BootID, Version: r.Version,
		Ready: r.reattached, Reattached: r.reattached, ResyncSeq: r.resyncSeq, At: now,
	}
	r.mu.Unlock()
	for _, h := range held {
		switch {
		case h.Item.Stage == "seeding":
			rec.Seeding++
		case h.Item.Status == download.StatusQueued:
			rec.Queued++
		case h.Item.Status == download.StatusDownloading:
			rec.Active++
		}
		if !h.Identified() {
			rec.UnidentifiedCount++
			if len(rec.Unidentified) < schema.MaxUnidentified {
				rec.Unidentified = append(rec.Unidentified, schema.UnidentifiedTransfer{
					DownloadID: h.Item.ID, Name: h.Name, AddedAt: h.Item.AddedAt, SizeBytes: h.Item.TotalBytes,
				})
			}
		}
	}
	if r.Free != nil {
		rec.ScratchFreeBytes, rec.PublishFreeBytes = r.Free()
	}
	rec.ProxyUDP = schema.ProxyUDPNotApplicable
	if r.Proxy != nil {
		rec.ProxyUDP = r.Proxy()
	}
	key := agentrecords.EngineKey(r.Client.UID, r.Ordinal)
	if _, err := r.Engines.Write(ctx, key, rec); err != nil {
		return fmt.Errorf("engine: engine record: %w", err)
	}
	return nil
}

// fingerprint is a record's change-worthy fields and its counters, apart.
func fingerprint(rec *schema.TransferRecord) (string, string) {
	fp := fmt.Sprintf("%s|%d|%s|%s|%t|%t|%t|%t|%s|%s|%t", rec.State, rec.Seq, rec.Stage, rec.EngineFailureReason,
		rec.HealthPaused, rec.SeedGoalReached, rec.CanBeRemoved, rec.Claimed, rec.OutputPath, rec.ContentRoot, rec.IsEncrypted)
	counters := fmt.Sprintf("%d|%d|%d|%d|%d|%d", rec.DownloadedBytes, rec.UploadedBytes, rec.RemainingBytes,
		rec.ProgressPercent, rec.SeedTimeSeconds, rec.RatioMilli)
	return fp, counters
}

// clampFiles cuts a transfer's file list to schema.MaxTransferFilesBytes of
// JSON, reporting a cut.
func clampFiles(files []download.File) ([]schema.TransferFile, bool) {
	out := make([]schema.TransferFile, 0, len(files))
	budget := schema.MaxTransferFilesBytes
	for _, f := range files {
		tf := schema.TransferFile{Path: f.Path, SizeBytes: f.SizeBytes, Skipped: f.Skipped}
		b, _ := json.Marshal(tf)
		if budget -= len(b) + 1; budget < 0 {
			return out, true
		}
		out = append(out, tf)
	}
	return out, false
}

func clampPercent(p int32) int32 { return min(max(p, 0), 100) }

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
