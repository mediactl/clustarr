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

package lifecycle

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// step runs one entry's row of §6.4 and reports whether it stays.
func (d *decider) step(e *catalogv1alpha1.DownloadEntry) bool {
	rec := d.recordOf(e)
	d.observe(e, rec)
	switch e.Phase {
	case "", commonv1.DownloadPhasePending:
		return d.pending(e)
	case commonv1.DownloadPhaseRemoving:
		return d.removing(e, rec)
	case commonv1.DownloadPhaseFailed, commonv1.DownloadPhaseBlocklisted:
		return d.terminal(e, rec)
	default:
		return d.live(e, rec)
	}
}

// pending is Pending -> Assigned (§6.4, §5.1): a client and an engine
// instance chosen and Ready, both pinned once; else the entry waits with the
// engine named.
func (d *decider) pending(e *catalogv1alpha1.DownloadEntry) bool {
	e.Phase = commonv1.DownloadPhasePending
	var (
		c  Client
		ok bool
	)
	if e.Client != "" {
		c, ok = d.clients[e.Client]
		ok = ok && c.Enabled && c.Protocol == e.Release.Protocol
	}
	if !ok {
		c, ok = ChooseClient(d.v.Clients, e.Release.Protocol, d.v.IndexerClient[e.Release.IndexerRef])
	}
	if !ok {
		e.Message = clampString(fmt.Sprintf("waiting for an enabled %s DownloadClient", e.Release.Protocol), MaxEntryMessage)
		d.waiting(e, catalogv1alpha1.DeliveryReasonNoCapableAgent, e.Message)
		d.at(d.now.Add(time.Minute))
		return true
	}
	ordinal, ok := ChooseOrdinal(c, d.v.Engines, e.Release.InfoHash, e.Release.GUID, d.now)
	if !ok {
		e.Message = clampString(fmt.Sprintf("waiting for an engine of DownloadClient %s to be ready", c.Name), MaxEntryMessage)
		d.waiting(e, catalogv1alpha1.DeliveryReasonEngineNotReady, e.Message)
		d.at(d.now.Add(RepublishEvery))
		return true
	}
	e.Client = c.Name
	e.Engine = EngineName(c.Name, ordinal)
	e.Phase = commonv1.DownloadPhaseAssigned
	e.Message = ""
	if e.GrabbedAt.IsZero() {
		e.GrabbedAt = metav1.NewTime(d.now)
	}
	d.issue(e)
	d.history(e, ActionGrabbed, "")
	if isDirect(e.Source) && e.Release.IndexerRef != "" {
		d.plan.DirectGrabs = append(d.plan.DirectGrabs, DirectGrab{
			IndexerRef: e.Release.IndexerRef, EntryUID: types.UID(e.UID), At: e.GrabbedAt.UTC(),
		})
	}
	return true
}

// isDirect is directgrab.IsDirectGrab's rule: every source but
// indexerDownload is fetched without rpc.indexarr.download, so its grab is
// counted at the Assigned edge (§6.11).
func isDirect(src commonv1.DownloadSource) bool {
	return src.IndexerDownload == nil && (src.MagnetURL != nil || src.TorrentURL != nil || src.NZBURL != nil)
}

// waiting renders a refused dispatch on e.
func (d *decider) waiting(e *catalogv1alpha1.DownloadEntry, reason, msg string) {
	e.Dispatch.Delivery = &catalogv1alpha1.DeliveryState{
		State: catalogv1alpha1.DeliveryWaiting, Reason: reason, Message: clampString(msg, 256),
	}
}

// live runs a transfer's rows from Assigned through Seeding.
func (d *decider) live(e *catalogv1alpha1.DownloadEntry, rec *schema.TransferRecord) bool {
	c := d.clients[e.Client]
	eng, _ := d.engineOf(e)

	if rec == nil || !heldAtBoot(rec, eng) {
		// No record at the engine's boot. Judged only once the engine
		// re-attached and finished the resync its client asked for
		// (§6.7, Review Focus 1): then the engine does not hold the
		// transfer, and it is re-added -- never dropped.
		if ok, settledAt := d.settled(e); ok && !e.Dispatch.DispatchedAt.After(settledAt) {
			e.Message = "the engine holds no transfer for this entry; adding it again"
			d.issue(e)
			return true
		}
		if rec != nil && rec.State == schema.TransferStateFailed {
			return d.failed(e, rec)
		}
		d.owe(e)
		return true
	}

	if rec.State == schema.TransferStateFailed || rec.EngineFailureReason.IsFailure() {
		return d.failed(e, rec)
	}

	done := imported(e)
	switch {
	case rec.HealthPaused && c.HealthAction == downloadv1alpha1.HealthActionDelete:
		d.toBlocklisted(e, commonv1.DownloadFailureMissingArticles, "the release's article health fell below the floor")
		return true
	case rec.HealthPaused:
		e.Phase = commonv1.DownloadPhasePaused
		e.Message = clampString(healthMessage(rec), MaxEntryMessage)
	case d.v.Intents.Paused[e.ID]:
		e.Phase = commonv1.DownloadPhasePaused
	case done:
		e.Phase = afterImport(e)
	default:
		e.Phase = phaseOfStage(rec.Stage, e.Phase)
	}

	switch e.Phase {
	case commonv1.DownloadPhaseQueued, commonv1.DownloadPhaseDownloading:
		if d.stalled(e, rec, c) {
			return true
		}
	case commonv1.DownloadPhaseCompleted:
		if e.CompletedAt == nil {
			t := metav1.NewTime(d.now)
			e.CompletedAt = &t
			d.history(e, ActionCompleted, "")
		}
		if d.importStep(e) {
			return true
		}
	case commonv1.DownloadPhaseSeeding, commonv1.DownloadPhaseImported:
		removeAfter := e.RemoveOnImport || c.RemoveCompleted
		switch {
		case e.Phase == commonv1.DownloadPhaseSeeding && rec.SeedGoalReached && removeAfter:
			d.toRemoving(e, "the seed goal is met")
			return true
		case e.Phase == commonv1.DownloadPhaseImported && removeAfter:
			d.toRemoving(e, "imported")
			return true
		}
	}
	d.owe(e)
	return true
}

// failed handles a transfer record that reports a failure (§6.4): a release
// fault blocklists it, a local fault fails it, and a payload that can no
// longer be fetched after the import drops the entry with SeedingLost.
func (d *decider) failed(e *catalogv1alpha1.DownloadEntry, rec *schema.TransferRecord) bool {
	reason := rec.EngineFailureReason
	if !reason.IsFailure() {
		// A failure with no reason is no evidence about the release: it is
		// taken as a local fault, never a block.
		reason = commonv1.DownloadFailureWriteError
	}
	if rec.Stage == "" && (e.Phase == commonv1.DownloadPhaseAssigned || e.Phase == commonv1.DownloadPhasePending) {
		d.event(EventWarning, ReasonDownloadAddFailed, "the engine could not add "+e.ID+": "+clampString(rec.Message, 512))
	}
	switch {
	case reason == commonv1.DownloadFailurePayloadUnavailable && imported(e):
		d.event(EventWarning, ReasonSeedingLost, "the payload of "+e.ID+" can no longer be fetched to seed it; dropping the entry")
		d.history(e, ActionRemoved, string(reason))
		return false
	case reason.IsReleaseFault():
		d.toBlocklisted(e, reason, "the engine reported "+string(reason))
	default:
		d.toFailed(e, reason, "the engine reported "+string(reason))
	}
	return true
}

// healthMessage names the health floor a usenet transfer fell under.
func healthMessage(rec *schema.TransferRecord) string {
	if h := rec.Health; h != nil {
		return fmt.Sprintf("paused: article health %d%% is below the floor (critical %d%%); resume it to continue",
			h.HealthPercent, h.CriticalHealthPercent)
	}
	return "paused: article health is below the floor; resume it to continue"
}

// phaseOfStage maps the record's stage (§6.4): fetchingMetadata is Queued,
// the transfer and its post-processing Downloading, done and seeding
// Completed (then the import); no stage yet keeps the phase.
func phaseOfStage(stage commonv1.DownloadStage, cur commonv1.DownloadPhase) commonv1.DownloadPhase {
	switch stage {
	case commonv1.DownloadStageFetchingMetadata:
		return commonv1.DownloadPhaseQueued
	case commonv1.DownloadStageTransferring, commonv1.DownloadStageVerifying, commonv1.DownloadStageRepairing,
		commonv1.DownloadStageExtracting, commonv1.DownloadStagePublishing:
		return commonv1.DownloadPhaseDownloading
	case commonv1.DownloadStageDone, commonv1.DownloadStageSeeding:
		return commonv1.DownloadPhaseCompleted
	}
	if cur == commonv1.DownloadPhasePaused || cur == "" {
		return commonv1.DownloadPhaseAssigned
	}
	return cur
}

// afterImport is an imported entry's phase: Seeding while a torrent seeds,
// Imported for usenet.
func afterImport(e *catalogv1alpha1.DownloadEntry) commonv1.DownloadPhase {
	if e.Release.Protocol == commonv1.ProtocolTorrent {
		return commonv1.DownloadPhaseSeeding
	}
	return commonv1.DownloadPhaseImported
}

// stalled is the manager's stall and download-timeout judgement (§3.5
// P111, R21) on the record's lastProgressAt and startedAt.
func (d *decider) stalled(e *catalogv1alpha1.DownloadEntry, rec *schema.TransferRecord, c Client) bool {
	if d.v.Intents.Paused[e.ID] {
		return false
	}
	st := c.StallTimeout
	if st == 0 && c.Protocol == commonv1.ProtocolTorrent {
		st = StallTimeout
	}
	if st > 0 {
		last := rec.AddedAt
		switch {
		case rec.LastProgressAt != nil:
			last = *rec.LastProgressAt
		case rec.StartedAt != nil:
			last = *rec.StartedAt
		}
		if !last.IsZero() {
			if d.now.Sub(last) > st {
				d.toBlocklisted(e, commonv1.DownloadFailureStalled, fmt.Sprintf("no progress for %s", st))
				return true
			}
			d.at(last.Add(st))
		}
	}
	if c.DownloadTimeout > 0 && rec.StartedAt != nil {
		if d.now.Sub(*rec.StartedAt) > c.DownloadTimeout {
			d.toBlocklisted(e, commonv1.DownloadFailureTimeout, fmt.Sprintf("not finished within %s", c.DownloadTimeout))
			return true
		}
		d.at(rec.StartedAt.Add(c.DownloadTimeout))
	}
	return false
}

// importStep runs a Completed entry's import (§6.9) from importplan's
// decision; it reports a transition that ended the pass for the entry.
func (d *decider) importStep(e *catalogv1alpha1.DownloadEntry) bool {
	dec, ok := d.v.Import[types.UID(e.UID)]
	if !ok {
		if imported(e) && filesLanded(e.Import.Files, d.v.Files[e.ID]) {
			d.toImported(e)
			return true
		}
		return false
	}
	sum := dec.Summary
	e.Import = &sum
	if dec.Dispatch != nil {
		dd := *dec.Dispatch
		dd.EntryUID = types.UID(e.UID)
		d.plan.Imports = append(d.plan.Imports, dd)
	}
	d.plan.Materialise = append(d.plan.Materialise, dec.Materialise...)
	d.at(dec.Due)
	switch dec.Verdict {
	case ImportImported:
		if filesLanded(sum.Files, d.v.Files[e.ID]) {
			d.toImported(e)
			return true
		}
	case ImportReleaseFault:
		d.toBlocklisted(e, commonv1.DownloadFailureImportRejected, clampString(sum.Message, 512))
		return true
	case ImportExpired:
		d.toFailed(e, commonv1.DownloadFailureImportExpired, "the held import expired after "+ImportHoldRetention.String())
		return true
	}
	return false
}

// filesLanded reports whether every MediaFile an import placed names its
// entry: want of them, counted have. An import that places no library file
// (an audio donor, which names its AudioGraft instead) has none to wait for;
// importplan says imported only once its AudioGraft names the donor.
func filesLanded(want int32, have int) bool {
	return want <= 0 || have >= int(want)
}

// Importable reports whether e is a Completed grab -- by its stored phase,
// or by rec, its transfer record, which completes it in the pass that reads
// it -- whose import importplan decides: until the entry reads Imported or
// Seeding, which needs every MediaFile its import placed.
func Importable(e *catalogv1alpha1.DownloadEntry, rec *schema.TransferRecord) bool {
	switch e.Phase {
	case commonv1.DownloadPhaseCompleted:
		return true
	case commonv1.DownloadPhaseAssigned, commonv1.DownloadPhaseQueued, commonv1.DownloadPhaseDownloading:
		return !imported(e) && rec != nil && phaseOfStage(rec.Stage, e.Phase) == commonv1.DownloadPhaseCompleted &&
			rec.State != schema.TransferStateFailed && !rec.EngineFailureReason.IsFailure()
	}
	return false
}

// NextSeq is a new dispatch seq above recordSeq and statusSeq (loop spec
// §2.3): importplan's for the inspect and execute tasks.
func NextSeq(recordSeq, statusSeq int64, now time.Time) int64 {
	return nextSeq(recordSeq, statusSeq, now)
}

// toImported is Completed -> Imported or Seeding, once every placed file
// has its MediaFile (§6.4): the command carries imported: true.
func (d *decider) toImported(e *catalogv1alpha1.DownloadEntry) {
	if e.Import == nil {
		e.Import = &catalogv1alpha1.DownloadImportSummary{}
	}
	e.Import.Phase = catalogv1alpha1.ImportPhaseImported
	if e.Import.ImportedAt == nil {
		t := metav1.NewTime(d.now)
		e.Import.ImportedAt = &t
	}
	e.Phase = afterImport(e)
	e.Message = ""
	d.issue(e)
	d.history(e, ActionImported, "")
}

// toRemoving moves e to Removing: the transfer goes, its data when
// removeDataOnDelete (the entry's effective value, or the remove intent's
// data flag).
func (d *decider) toRemoving(e *catalogv1alpha1.DownloadEntry, why string) {
	if e.Phase == commonv1.DownloadPhaseRemoving {
		return
	}
	e.Phase = commonv1.DownloadPhaseRemoving
	e.Message = clampString("removing: "+why, MaxEntryMessage)
	d.issue(e)
}

// toFailed moves e to Failed with a local fault (or importExpired): the
// transfer goes; no blocklist, no redownload (Radarr).
func (d *decider) toFailed(e *catalogv1alpha1.DownloadEntry, reason commonv1.DownloadFailureReason, msg string) {
	e.Phase = commonv1.DownloadPhaseFailed
	e.FailureReason = reason
	e.Message = clampString(msg, MaxEntryMessage)
	d.issue(e)
	d.history(e, ActionFailed, string(reason))
}

// toBlocklisted moves e to Blocklisted (§6.4, §6.14): the block it owes the
// release index at a new seq, scoped by reason; the transfer goes; history
// failed; and, by R25, a redownload search for each covered item while the
// tombstone stands.
func (d *decider) toBlocklisted(e *catalogv1alpha1.DownloadEntry, reason commonv1.DownloadFailureReason, msg string) {
	var prev int64
	if e.Block != nil {
		prev = e.Block.Seq
	}
	e.Phase = commonv1.DownloadPhaseBlocklisted
	e.FailureReason = reason
	e.Message = clampString(msg, MaxEntryMessage)
	e.Block = &catalogv1alpha1.EntryBlock{Scope: ScopeFor(reason), Reason: reason, Seq: nextSeq(0, prev, d.now)}
	d.issue(e)
	d.history(e, ActionFailed, string(reason))
	d.history(e, ActionBlocklisted, string(reason))
}

// removing waits for the record to read removed at the absent command's
// seq, then drops the entry (after removing its data when asked); an entry
// whose engine is gone is dropped EngineTeardownTimeout after the
// transition (R-6), and an entry whose settled engine holds no record never
// had a transfer to remove.
func (d *decider) removing(e *catalogv1alpha1.DownloadEntry, rec *schema.TransferRecord) bool {
	if d.transferGone(e, rec) {
		return d.finishRemoval(e)
	}
	d.owe(e)
	return true
}

// transferGone reports positive evidence that e's transfer no longer
// exists: never assigned; the record removed at the absent command's seq;
// a settled engine with no record at its boot; or the engine gone past
// EngineTeardownTimeout (Event EngineGone).
func (d *decider) transferGone(e *catalogv1alpha1.DownloadEntry, rec *schema.TransferRecord) bool {
	if e.Engine == "" {
		return true
	}
	if rec != nil && rec.State == schema.TransferStateRemoved && rec.Seq >= e.Dispatch.Seq {
		return true
	}
	eng, _ := d.engineOf(e)
	if ok, _ := d.settled(e); ok && !heldAtBoot(rec, eng) && !e.Dispatch.InFlight() {
		return true
	}
	if gone, why := d.engineGone(e); gone {
		end := e.Dispatch.DispatchedAt.Add(EngineTeardownTimeout)
		if d.now.Before(end) {
			d.at(end)
			return false
		}
		d.event(EventWarning, ReasonEngineGone, fmt.Sprintf("%s: %s for %s; dropping %s on its behalf", e.Engine, why, EngineTeardownTimeout, e.ID))
		return true
	}
	return false
}

// finishRemoval removes e's output path when asked, then drops it.
func (d *decider) finishRemoval(e *catalogv1alpha1.DownloadEntry) bool {
	if e.RemoveDataOnDelete && e.OutputPath != "" && !d.v.Removed[e.OutputPath] {
		d.plan.Removals = append(d.plan.Removals, e.OutputPath)
		d.at(d.now.Add(time.Second))
		return true
	}
	d.history(e, ActionRemoved, string(e.FailureReason))
	return false
}

// terminal runs Failed and Blocklisted: the block's confirmation, the
// transfer's removal, the redownload search it owes (R25) and the
// quarantine, then the drop.
func (d *decider) terminal(e *catalogv1alpha1.DownloadEntry, rec *schema.TransferRecord) bool {
	if d.unblocked != nil && d.unblocked(e) {
		e.Block = nil
		if d.transferGone(e, rec) {
			return d.finishRemoval(e)
		}
		d.owe(e)
		return true
	}
	confirmed := true
	if e.Phase == commonv1.DownloadPhaseBlocklisted && e.Block != nil {
		confirmed = d.confirmBlock(e)
	}
	keep := d.redownload(e)
	if !d.transferGone(e, rec) {
		d.owe(e)
		return true
	}
	if !confirmed || keep {
		return true
	}
	if e.Phase == commonv1.DownloadPhaseBlocklisted && e.Block != nil && e.Block.ConfirmedAt != nil {
		end := e.Block.ConfirmedAt.Add(BlockQuarantine)
		if d.now.Before(end) {
			d.at(end)
			return true
		}
	}
	return d.finishRemoval(e)
}

// confirmBlock records the release index's confirmation of e's block from
// the book (R9), or owes the call.
func (d *decider) confirmBlock(e *catalogv1alpha1.DownloadEntry) bool {
	if e.Block.ConfirmedAt != nil {
		return true
	}
	if r, ok := d.v.Blocks[types.UID(e.UID)]; ok && r.Seq == e.Block.Seq && (r.Applied || r.Stale) {
		at := r.At
		if at.IsZero() {
			at = d.now
		}
		t := metav1.NewTime(at.UTC())
		e.Block.ConfirmedAt = &t
		return true
	}
	blockedAt := e.Dispatch.DispatchedAt.UTC()
	if blockedAt.IsZero() {
		blockedAt = d.now
	}
	d.plan.Blocks = append(d.plan.Blocks, BlockCall{EntryUID: types.UID(e.UID), Req: schema.BlocklistRequest{
		Op:        schema.BlocklistOpBlock,
		Scope:     scopeString(e.Block.Scope, d.v.Owner),
		InfoHash:  e.Release.InfoHash,
		Indexer:   e.Release.IndexerRef,
		GUID:      e.Release.GUID,
		Title:     e.Release.Title,
		Protocol:  string(e.Release.Protocol),
		Reason:    string(e.Block.Reason),
		EntryID:   e.ID,
		Seq:       e.Block.Seq,
		BlockedAt: blockedAt,
		Until:     blockedAt.Add(BlocklistTTL),
	}})
	d.at(d.now.Add(RepublishEvery))
	return false
}

// redownload is R25: a Blocklisted entry, or a Failed one that is neither a
// local fault nor an expired hold, younger than RedownloadWindow, asks a
// search of each covered item whose last search dispatch is older than the
// failure, and stays until each was dispatched.
func (d *decider) redownload(e *catalogv1alpha1.DownloadEntry) bool {
	if e.Phase == commonv1.DownloadPhaseFailed &&
		(e.FailureReason.IsLocalFault() || e.FailureReason == commonv1.DownloadFailureImportExpired || e.FailureReason == "") {
		return false
	}
	failedAt := e.Dispatch.DispatchedAt.UTC()
	if failedAt.IsZero() || d.now.Sub(failedAt) >= RedownloadWindow {
		return false
	}
	covered := d.v.Covered[types.UID(e.UID)]
	if len(covered) == 0 {
		covered = []schema.ItemRef{d.v.Owner}
	}
	owed := false
	for _, it := range covered {
		key := schema.ItemRef{Kind: it.Kind, Ref: schema.Ref{Namespace: it.Namespace, Name: it.Name, UID: it.UID}}
		if s, ok := d.v.Searched[key]; ok && !s.Before(failedAt) {
			continue
		}
		d.plan.Redownloads = append(d.plan.Redownloads, it)
		owed = true
	}
	if owed {
		d.at(failedAt.Add(RedownloadWindow))
	}
	return owed
}
