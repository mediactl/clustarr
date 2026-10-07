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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// decider is one Decide's state.
type decider struct {
	v       View
	now     time.Time
	plan    Plan
	clients map[string]Client
	// unblocked matches the Blocklisted tombstones an unblock the index
	// confirmed releases this pass.
	unblocked func(e *catalogv1alpha1.DownloadEntry) bool
}

// Decide runs the grab state machine over one owner's entries (§6.4): the
// stored ones, the grab stage's new ones and the transfers no entry claims,
// under the owner's intents, and returns the next entries and every effect
// owed after the apply.
func Decide(v View) Plan {
	d := &decider{v: v, now: v.Now.UTC(), clients: map[string]Client{}}
	for _, c := range v.Clients {
		d.clients[c.Name] = c
	}
	d.plan.Nonces = v.Nonces
	d.plan.Issued = map[types.UID]IssuedCommand{}

	entries := make([]catalogv1alpha1.DownloadEntry, 0, len(v.Stored)+len(v.NewEntries))
	for i := range v.Stored {
		entries = append(entries, *v.Stored[i].DeepCopy())
	}
	if !v.Deleting {
		for i := range v.NewEntries {
			ne := v.NewEntries[i].DeepCopy()
			if hasEntry(entries, ne.ID, ne.UID) || (v.EntryCap > 0 && len(entries) >= v.EntryCap) {
				continue
			}
			if ne.Phase == "" {
				ne.Phase = commonv1.DownloadPhasePending
			}
			entries = append(entries, *ne)
		}
	}

	d.intents(entries)
	if v.Deleting {
		for i := range entries {
			if live(&entries[i]) {
				d.toRemoving(&entries[i], "the owner is being deleted")
			}
		}
	}

	kept := make([]catalogv1alpha1.DownloadEntry, 0, len(entries))
	for i := range entries {
		if d.step(&entries[i]) {
			kept = append(kept, entries[i])
		}
	}
	kept = d.converge(kept)

	d.plan.Entries = kept
	d.plan.Phase = activePhase(kept)
	want := len(kept) > 0
	d.plan.Finalizer = &want
	return d.plan
}

// live reports an entry before Removing that is not terminal: it still
// wants its transfer present.
func live(e *catalogv1alpha1.DownloadEntry) bool {
	switch e.Phase {
	case commonv1.DownloadPhaseRemoving, commonv1.DownloadPhaseFailed, commonv1.DownloadPhaseBlocklisted:
		return false
	}
	return true
}

func hasEntry(entries []catalogv1alpha1.DownloadEntry, id, uid string) bool {
	for i := range entries {
		if (id != "" && entries[i].ID == id) || (uid != "" && entries[i].UID == uid) {
			return true
		}
	}
	return false
}

func findEntry(entries []catalogv1alpha1.DownloadEntry, id string) int {
	for i := range entries {
		if entries[i].ID == id {
			return i
		}
	}
	return -1
}

// activePhase is the owner's downloadPhase: the oldest non-terminal,
// non-donor entry's phase by grabbedAt, the id breaking a tie
// (rollup.ActiveEntry's rule, restated: rollup links controller-runtime).
func activePhase(entries []catalogv1alpha1.DownloadEntry) commonv1.DownloadPhase {
	var best *catalogv1alpha1.DownloadEntry
	for i := range entries {
		e := &entries[i]
		if !nonTerminal(e) || e.Purpose == commonv1.DownloadPurposeAudioDonor {
			continue
		}
		if best == nil || e.GrabbedAt.Before(&best.GrabbedAt) || (e.GrabbedAt.Equal(&best.GrabbedAt) && e.ID < best.ID) {
			best = e
		}
	}
	if best == nil {
		return ""
	}
	return best.Phase
}

// nonTerminal is rollup.EntryNonTerminal's rule.
func nonTerminal(e *catalogv1alpha1.DownloadEntry) bool {
	if imp := e.Import; imp != nil && (imp.Phase == catalogv1alpha1.ImportPhaseHeld || imp.Phase == catalogv1alpha1.ImportPhaseExpired) {
		return false
	}
	switch e.Phase {
	case commonv1.DownloadPhaseImported, commonv1.DownloadPhaseFailed,
		commonv1.DownloadPhaseBlocklisted, commonv1.DownloadPhaseRemoving:
		return false
	}
	return true
}

// imported reports the entry's import finished.
func imported(e *catalogv1alpha1.DownloadEntry) bool {
	return e.Import != nil && e.Import.Phase == catalogv1alpha1.ImportPhaseImported
}

// at shortens the plan's Due to t when t is earlier (and in the future).
func (d *decider) at(t time.Time) {
	if t.IsZero() {
		return
	}
	if !t.After(d.now) {
		t = d.now.Add(time.Second)
	}
	if d.plan.Due.IsZero() || t.Before(d.plan.Due) {
		d.plan.Due = t
	}
}

func (d *decider) event(typ, reason, msg string) {
	d.plan.Events = append(d.plan.Events, Event{Recorder: RecorderDownloads, Type: typ, Reason: reason, Message: msg})
}

// history records one DownloadEvent for e.
func (d *decider) history(e *catalogv1alpha1.DownloadEntry, action, reason string) {
	media := commonv1.MediaRef{Kind: d.v.Kind, Name: d.v.Owner.Name}
	evt := schema.DownloadEvent{
		DownloadRef: schema.Ref{Namespace: d.v.Owner.Namespace, Name: e.ID, UID: e.UID},
		Media:       media,
		Action:      action,
		Protocol:    e.Release.Protocol,
		Title:       e.Release.Title,
		InfoHash:    e.Release.InfoHash,
		SizeBytes:   e.Release.SizeBytes,
		Reason:      reason,
		OutputPath:  e.OutputPath,
		Purpose:     string(e.Purpose),
		At:          d.now,
	}
	if e.Client != "" {
		evt.ClientRef = &schema.Ref{Namespace: d.v.Owner.Namespace, Name: e.Client}
	}
	d.plan.History = append(d.plan.History, evt)
}

// recordOf is e's transfer record, if any.
func (d *decider) recordOf(e *catalogv1alpha1.DownloadEntry) *schema.TransferRecord {
	t, ok := d.v.Transfers[types.UID(e.UID)]
	if !ok {
		return nil
	}
	r := t.Record
	return &r
}

// engineOf is e's pinned engine, if it reported.
func (d *decider) engineOf(e *catalogv1alpha1.DownloadEntry) (Engine, bool) {
	if e.Engine == "" {
		return Engine{}, false
	}
	eng, ok := d.v.Engines[e.Engine]
	return eng, ok
}

// engineGone reports that no engine will act on e's commands (§6.8,
// teardown.go's rule): never pinned, its DownloadClient gone, its engine not
// Ready on a fresh record, or its ordinal at or past the replicas.
func (d *decider) engineGone(e *catalogv1alpha1.DownloadEntry) (bool, string) {
	if e.Engine == "" {
		return true, "the entry was never assigned to an engine"
	}
	c, ok := d.clients[e.Client]
	if !ok {
		return true, "DownloadClient " + e.Client + " no longer exists"
	}
	if _, ordinal, ok := splitEngine(e.Engine); ok && ordinal >= max(c.Replicas, 1) {
		return true, "engine " + e.Engine + " is past its DownloadClient's replicas"
	}
	eng, ok := d.engineOf(e)
	if !ok || !engineReady(eng, d.now) {
		return true, "engine " + e.Engine + " is not ready"
	}
	return false, ""
}

// settled reports that e's engine re-attached at its boot, reached the
// resync its client asked for, and ResyncSettle passed since: from then on
// every transfer it holds has a record at that boot (§6.7).
func (d *decider) settled(e *catalogv1alpha1.DownloadEntry) (bool, time.Time) {
	eng, ok := d.engineOf(e)
	if !ok || !eng.Reattached || eng.ReattachedAt.IsZero() {
		return false, time.Time{}
	}
	c, ok := d.clients[e.Client]
	if !ok || eng.ResyncSeq < c.ResyncSeq {
		return false, time.Time{}
	}
	at := eng.ReattachedAt.Add(ResyncSettle)
	if d.now.Before(at) {
		d.at(at)
		return false, at
	}
	return true, at
}

// heldAtBoot reports that rec is the engine's report of the transfer at its
// current boot (state present or failed).
func heldAtBoot(rec *schema.TransferRecord, eng Engine) bool {
	return rec != nil && rec.State != schema.TransferStateRemoved && (eng.BootID == "" || rec.BootID == eng.BootID)
}

// splitEngine reads "<client>-<ordinal>", splitting at the last hyphen.
func splitEngine(engine string) (string, int32, bool) {
	i := strings.LastIndex(engine, "-")
	if i <= 0 || i == len(engine)-1 {
		return "", 0, false
	}
	var n int32
	for _, r := range engine[i+1:] {
		if r < '0' || r > '9' {
			return "", 0, false
		}
		n = n*10 + (r - '0')
	}
	return engine[:i], n, true
}

// observe incorporates e's transfer record into it: the seq it answered, its
// stage, output path, timestamps and message.
func (d *decider) observe(e *catalogv1alpha1.DownloadEntry, rec *schema.TransferRecord) {
	if rec == nil {
		return
	}
	if rec.Seq > e.Dispatch.Seq {
		// The engine holds a later seq than status (an older status
		// restored): adopt it, so the next command is above it.
		e.Dispatch.Seq = rec.Seq
	}
	if rec.Seq > e.Dispatch.AnsweredSeq {
		e.Dispatch.AnsweredSeq = rec.Seq
	}
	if !e.Dispatch.InFlight() {
		e.Dispatch.Delivery = nil
	}
	if rec.Stage != "" {
		e.Stage = rec.Stage
	}
	if rec.OutputPath != "" {
		e.OutputPath = clampString(rec.OutputPath, 4096)
	}
	if rec.StartedAt != nil && e.StartedAt == nil {
		t := metav1.NewTime(rec.StartedAt.UTC())
		e.StartedAt = &t
	}
	if rec.CompletedAt != nil && e.CompletedAt == nil {
		t := metav1.NewTime(rec.CompletedAt.UTC())
		e.CompletedAt = &t
	}
	if rec.SeedGoalReached && e.SeedGoalMetAt == nil {
		t := metav1.NewTime(d.now)
		e.SeedGoalMetAt = &t
		d.history(e, ActionSeedGoalMet, "")
	}
	if rec.Message != "" {
		e.Message = clampString(rec.Message, MaxEntryMessage)
	}
}

// desired is the transfer's whole desired state for e (§6.7), without the
// seq and issue time.
func (d *decider) desired(e *catalogv1alpha1.DownloadEntry) schema.EngineCommand {
	want := schema.EngineDesiredPresent
	if !live(e) {
		want = schema.EngineDesiredAbsent
	}
	src := e.Source
	cmd := schema.EngineCommand{
		Owner:    d.v.Owner,
		Entry:    schema.EntryRef{ID: e.ID, UID: e.UID},
		Desired:  want,
		Protocol: e.Release.Protocol,
		Source:   &src,
		Release: schema.CommandRelease{
			Title: e.Release.Title, GUID: e.Release.GUID, IndexerRef: e.Release.IndexerRef, SizeBytes: e.Release.SizeBytes,
		},
		RemoveOnImport: e.RemoveOnImport,
		SeedCriteria:   e.SeedCriteria,
		Imported:       imported(e),
		Claim:          d.claimOf(e),
	}
	if want == schema.EngineDesiredAbsent {
		cmd.RemoveData = e.RemoveDataOnDelete
	}
	if src.ExpectedInfoHash != nil {
		cmd.ExpectedInfoHash = *src.ExpectedInfoHash
	} else {
		cmd.ExpectedInfoHash = e.Release.InfoHash
	}
	if c, ok := d.clients[e.Client]; ok {
		cmd.Category = c.Categories[string(d.v.Kind)]
		if cmd.SeedCriteria == nil {
			cmd.SeedCriteria = c.Seed
		}
	}
	if len(e.Episodes) > 0 || len(e.Issues) > 0 {
		sel := &schema.TransferSelection{Issues: append([]string(nil), e.Issues...)}
		for _, n := range e.Episodes {
			sel.Episodes = append(sel.Episodes, schema.EpisodeNo{Season: n.Season, Number: n.Number})
		}
		cmd.Selection = sel
	}
	if d.v.Intents.Paused[e.ID] {
		cmd.Paused = true
	}
	if p := d.v.Intents.Priority[e.ID]; p != "" {
		cmd.Priority = commonv1.DownloadPriority(p)
	}
	if r := d.v.Intents.Resume; r != nil && r.ID == e.ID && d.plan.Nonces.Resume == r.Nonce {
		cmd.HealthOverride = r.Nonce
	}
	if e.Import != nil && e.Import.ImportedAt != nil {
		t := e.Import.ImportedAt.UTC()
		cmd.ImportedAt = &t
	}
	return cmd
}

// claimOf is the claim e's transfer is added under (§6.7, "Claims").
func (d *decider) claimOf(e *catalogv1alpha1.DownloadEntry) schema.TransferClaim {
	return schema.TransferClaim{
		Owner:              d.v.Owner,
		ProviderID:         d.v.ProviderID,
		Entry:              schema.EntryRef{ID: e.ID, UID: e.UID},
		InfoHash:           e.Release.InfoHash,
		Indexer:            e.Release.IndexerRef,
		GUID:               e.Release.GUID,
		Title:              e.Release.Title,
		Purpose:            e.Purpose,
		RemoveDataOnDelete: e.RemoveDataOnDelete,
		Imported:           imported(e),
	}
}

// hashOf is a command's desired state, by hash: everything but the seq and
// the issue time.
func hashOf(cmd schema.EngineCommand) string {
	cmd.Seq, cmd.IssuedAt = 0, time.Time{}
	b, _ := json.Marshal(cmd)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

// issue publishes e's desired state at a new seq (§6.7: status first, then
// the command; the seq is above the record's and the status's).
func (d *decider) issue(e *catalogv1alpha1.DownloadEntry) {
	if e.Engine == "" {
		// Nothing holds a transfer: there is nobody to command. The
		// transition time still dates the failure (R25).
		e.Dispatch.DispatchedAt = metav1.NewTime(d.now)
		return
	}
	var recSeq int64
	rec := d.recordOf(e)
	if rec != nil {
		recSeq = rec.Seq
	}
	seq := nextSeq(recSeq, e.Dispatch.Seq, d.now)
	e.Dispatch.Seq = seq
	e.Dispatch.DispatchedAt = metav1.NewTime(d.now)
	e.Dispatch.Destination = e.Engine
	e.Dispatch.Delivery = &catalogv1alpha1.DeliveryState{State: catalogv1alpha1.DeliveryPublished}
	d.emit(e, rec, false)
}

// emit renders e's command at its dispatch seq.
func (d *decider) emit(e *catalogv1alpha1.DownloadEntry, rec *schema.TransferRecord, retry bool) {
	cmd := d.desired(e)
	cmd.Seq = e.Dispatch.Seq
	cmd.IssuedAt = e.Dispatch.DispatchedAt.UTC()
	client, ordinal, _ := splitEngine(e.Engine)
	c := Command{Engine: e.Engine, Client: client, Ordinal: ordinal, Cmd: cmd, Retry: retry}
	if eng, ok := d.engineOf(e); ok && eng.BootID != "" && (rec == nil || rec.BootID != eng.BootID) {
		c.BootID = eng.BootID
	}
	d.plan.Commands = append(d.plan.Commands, c)
	d.plan.Issued[types.UID(e.UID)] = IssuedCommand{Seq: cmd.Seq, Hash: hashOf(cmd)}
	d.at(d.now.Add(RepublishEvery))
}

// owe keeps e's command delivered (§6.4, "Every effect is owed until the
// record shows it"): a desired state that changed since the seq was issued
// (or that the book does not know, after a leader change) is issued at a new
// seq; an owed seq is republished under its Msg-Id inside RepublishWindow
// and at a new seq past it.
func (d *decider) owe(e *catalogv1alpha1.DownloadEntry) {
	if e.Engine == "" {
		return
	}
	rec := d.recordOf(e)
	hash := hashOf(d.desired(e))
	book, known := d.v.Issued[types.UID(e.UID)]
	if issued, ok := d.plan.Issued[types.UID(e.UID)]; ok && issued.Seq == e.Dispatch.Seq {
		return // issued this pass
	}
	answered := rec != nil && rec.Seq >= e.Dispatch.Seq
	switch {
	case !known || book.Seq != e.Dispatch.Seq:
		if answered && !live(e) && rec.State == schema.TransferStateRemoved {
			return // the transfer is gone at this seq: nothing to say
		}
		d.issue(e)
	case book.Hash != hash:
		d.issue(e)
	case answered:
		d.plan.Issued[types.UID(e.UID)] = book
	case d.now.Sub(e.Dispatch.DispatchedAt.Time) < RepublishWindow:
		d.emit(e, rec, false)
	default:
		d.event(EventWarning, ReasonCommandRetrying, "the command to "+e.Engine+" for "+e.ID+" was not acknowledged in time; issuing it again")
		d.issue(e)
		d.plan.Commands[len(d.plan.Commands)-1].Retry = true
	}
}

// clampString cuts s to at most n bytes on a rune boundary.
func clampString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
