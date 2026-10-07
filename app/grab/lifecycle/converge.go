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
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Pending reports annotation a's one-shot value as not yet handled against
// recorded (the owner's downloadNonces field for it).
func (in Intents) Pending(a, recorded string) bool { return in.pending(a, recorded) }

// intents applies the owner's one-shot intents to entries (§6.10): each is
// recorded in Nonces on the pass that handles it; an invalid one with a
// Warning InvalidAnnotation; an unblock only once the index confirmed (R9).
func (d *decider) intents(entries []catalogv1alpha1.DownloadEntry) {
	in := d.v.Intents
	n := &d.plan.Nonces

	for _, inv := range in.Invalid {
		rec := nonceField(n, inv.Annotation)
		if rec == nil || !in.pending(inv.Annotation, *rec) {
			continue
		}
		*rec = in.NonceKey(inv.Annotation)
		d.event(EventWarning, ReasonInvalidAnnotation, inv.Annotation+": "+inv.Reason)
	}

	if r := in.Remove; r != nil && in.pending(catalogv1alpha1.AnnotationDownloadRemove, n.Remove) {
		n.Remove = r.Nonce
		if i := findEntry(entries, r.ID); i < 0 {
			d.event(EventNormal, ReasonNothingToRemove, "no download "+r.ID+" to remove")
		} else {
			e := &entries[i]
			e.RemoveDataOnDelete = r.Data
			switch {
			case r.Blocklist && e.Phase != commonv1.DownloadPhaseBlocklisted:
				d.toBlocklisted(e, commonv1.DownloadFailureManual, "removed and blocklisted by a person")
			case live(e):
				d.toRemoving(e, "removed by a person")
			default:
				// Already Failed or Blocklisted: the transfer's removal is
				// owed already; the data flag takes effect on it.
				d.issue(e)
			}
		}
	}

	if r := in.Resume; r != nil && in.pending(catalogv1alpha1.AnnotationDownloadResume, n.Resume) {
		n.Resume = r.Nonce
		if i := findEntry(entries, r.ID); i < 0 || !live(&entries[i]) {
			d.event(EventNormal, ReasonNothingToResume, "no live download "+r.ID+" to resume")
		} else {
			// The command's healthOverride is the nonce from now on, which
			// releases a health hold.
			d.issue(&entries[i])
		}
	}

	if r := in.Import; r != nil && in.pending(catalogv1alpha1.AnnotationDownloadImport, n.Import) {
		n.Import = r.Nonce
		if findEntry(entries, r.ID) < 0 {
			d.event(EventWarning, ReasonInvalidAnnotation, "download.clustarr.io/import names no download "+r.ID)
		}
		// The stage hands the intent to importplan, which re-issues the
		// inspect with the target and override (A3.8).
	}

	if u := in.Unblock; u != nil && in.pending(catalogv1alpha1.AnnotationDownloadUnblock, n.Unblock) {
		if d.v.Unblocked[u.Nonce] {
			n.Unblock = u.Nonce
			d.unblocked = func(e *catalogv1alpha1.DownloadEntry) bool {
				if e.Phase != commonv1.DownloadPhaseBlocklisted {
					return false
				}
				if u.InfoHash != "" {
					return strings.EqualFold(e.Release.InfoHash, u.InfoHash)
				}
				return e.Release.IndexerRef == u.Indexer && e.Release.GUID == u.GUID
			}
		} else {
			scope := schema.BlockScopeOf(d.v.Owner)
			if u.Global {
				scope = schema.BlockScopeGlobal
			}
			d.plan.Blocks = append(d.plan.Blocks, BlockCall{Nonce: u.Nonce, Req: schema.BlocklistRequest{
				Op: schema.BlocklistOpUnblock, Scope: scope,
				InfoHash: u.InfoHash, Indexer: u.Indexer, GUID: u.GUID,
				Seq: nextSeq(0, 0, d.now), BlockedAt: d.now,
			}})
			d.at(d.now.Add(RepublishEvery))
		}
	}
}

// nonceField is the downloadNonces field annotation a records into.
func nonceField(n *catalogv1alpha1.DownloadNonces, a string) *string {
	switch a {
	case catalogv1alpha1.AnnotationDownloadRemove:
		return &n.Remove
	case catalogv1alpha1.AnnotationDownloadResume:
		return &n.Resume
	case catalogv1alpha1.AnnotationDownloadImport:
		return &n.Import
	case catalogv1alpha1.AnnotationDownloadUnblock:
		return &n.Unblock
	}
	return nil
}

// converge decides each transfer no entry claims whose claim names this
// owner (§6.7's evidence table): the same owner -- the same UID, or after an
// etcd restore the same name with an equal or absent provider id -- either
// still wants it, and the entry is re-created under the claim's id and uid,
// or no longer does, and it is removed with the claim's data rule; a
// different provider id is another item reusing the name, removed only on
// the APIReader's verification. A claim whose owner is NotFound is the
// stage's (OwnerGone).
func (d *decider) converge(entries []catalogv1alpha1.DownloadEntry) []catalogv1alpha1.DownloadEntry {
	for _, t := range d.v.Unclaimed {
		rec := t.Record
		if rec.Claim == nil || rec.Claimed || rec.State != schema.TransferStatePresent {
			continue
		}
		claim := *rec.Claim
		if hasEntry(entries, "", claim.Entry.UID) {
			continue
		}
		same := claim.Owner.UID == d.v.Owner.UID ||
			(claim.Owner.Name == d.v.Owner.Name && (claim.ProviderID == "" || d.v.ProviderID == "" || claim.ProviderID == d.v.ProviderID))
		if !same {
			if d.v.ProviderVerified {
				d.removeUnclaimed(rec, claim, "the transfer's owner is another item now (provider id "+claim.ProviderID+" against "+d.v.ProviderID+")")
			}
			continue
		}
		wanted, why := d.stillWants(claim, rec, entries)
		if !wanted || d.v.Deleting || (d.v.EntryCap > 0 && len(entries) >= d.v.EntryCap) {
			if why == "" {
				why = "the owner no longer wants it"
			}
			d.removeUnclaimed(rec, claim, why)
			continue
		}
		if e, ok := d.recreate(rec, claim); ok {
			d.issue(&e)
			entries = append(entries, e)
		}
	}
	return entries
}

// stillWants is the grab planner's answer when it is set (A4.1), else A3's
// default: the owner is monitored and either holds no live entry of the
// claim's purpose and has no file, or the claim is imported with its seed
// goal not met (the seeding obligation).
func (d *decider) stillWants(claim schema.TransferClaim, rec schema.TransferRecord, entries []catalogv1alpha1.DownloadEntry) (bool, string) {
	if d.v.StillWants != nil {
		return d.v.StillWants(claim)
	}
	if !d.v.Monitored {
		return false, "the owner is not monitored"
	}
	if claim.Imported && !rec.SeedGoalReached {
		return true, "its seed goal is not met"
	}
	for i := range entries {
		if entries[i].Purpose == claim.Purpose && live(&entries[i]) {
			return false, "the owner has another grab of it in flight"
		}
	}
	if d.v.HasFile {
		return false, "the owner has a file"
	}
	return true, ""
}

// removeUnclaimed commands an unclaimed transfer absent at the seq after its
// record's, with the claim's data rule; the seq is deterministic, so a pass
// that says it again is absorbed by the duplicate window.
func (d *decider) removeUnclaimed(rec schema.TransferRecord, claim schema.TransferClaim, why string) {
	client, ordinal, _ := splitEngine(rec.Engine)
	cmd := schema.EngineCommand{
		Seq: rec.Seq + 1, Owner: claim.Owner, Entry: claim.Entry, Desired: schema.EngineDesiredAbsent,
		RemoveData: claim.RemoveDataOnDelete, Claim: claim, IssuedAt: d.now,
		Release: schema.CommandRelease{Title: claim.Title, GUID: claim.GUID, IndexerRef: claim.Indexer},
	}
	c := Command{Engine: rec.Engine, Client: client, Ordinal: ordinal, Cmd: cmd}
	if eng, ok := d.v.Engines[rec.Engine]; ok && eng.BootID != "" && eng.BootID != rec.BootID {
		c.BootID = eng.BootID
	}
	d.plan.Commands = append(d.plan.Commands, c)
	d.event(EventNormal, ReasonTransferOwnerGone, "removing the transfer "+claim.Entry.ID+" on "+rec.Engine+": "+why)
	d.at(d.now.Add(RepublishEvery))
}

// recreate rebuilds an entry from a claimed transfer's record, under the
// claim's id and uid, in the phase the record implies.
func (d *decider) recreate(rec schema.TransferRecord, claim schema.TransferClaim) (catalogv1alpha1.DownloadEntry, bool) {
	clientName, _, ok := splitEngine(rec.Engine)
	if !ok {
		return catalogv1alpha1.DownloadEntry{}, false
	}
	c, ok := d.clients[clientName]
	if !ok {
		return catalogv1alpha1.DownloadEntry{}, false
	}
	src, ok := sourceOf(claim)
	if !ok {
		return catalogv1alpha1.DownloadEntry{}, false
	}
	e := catalogv1alpha1.DownloadEntry{
		ID: claim.Entry.ID, UID: claim.Entry.UID, Purpose: claim.Purpose,
		Release: catalogv1alpha1.DownloadRelease{
			Title: clampString(claim.Title, 512), GUID: claim.GUID, IndexerRef: claim.Indexer,
			InfoHash: strings.ToLower(claim.InfoHash), Protocol: c.Protocol,
		},
		Source:             src,
		Client:             c.Name,
		Engine:             rec.Engine,
		GrabbedBy:          commonv1.GrabSourcePush,
		GrabbedAt:          metav1.NewTime(rec.AddedAt.UTC()),
		RemoveDataOnDelete: claim.RemoveDataOnDelete,
		Phase:              phaseOfStage(rec.Stage, commonv1.DownloadPhaseAssigned),
		Message:            "re-created from its transfer's claim",
	}
	if e.GrabbedAt.IsZero() {
		e.GrabbedAt = metav1.NewTime(d.now)
	}
	if claim.Imported {
		t := metav1.NewTime(d.now)
		e.Import = &catalogv1alpha1.DownloadImportSummary{Phase: catalogv1alpha1.ImportPhaseImported, ImportedAt: &t}
		e.Phase = afterImport(&e)
	}
	e.Dispatch.Seq = rec.Seq
	e.Dispatch.AnsweredSeq = rec.Seq
	return e, true
}

// sourceOf is a re-created entry's source: the indexer's release when the
// claim names one, else a magnet of its info hash.
func sourceOf(claim schema.TransferClaim) (commonv1.DownloadSource, bool) {
	if claim.Indexer != "" && claim.GUID != "" {
		return commonv1.DownloadSource{IndexerDownload: &commonv1.IndexerDownload{IndexerRef: claim.Indexer, GUID: claim.GUID}}, true
	}
	if claim.InfoHash != "" {
		m := "magnet:?xt=urn:btih:" + strings.ToLower(claim.InfoHash)
		return commonv1.DownloadSource{MagnetURL: &m}, true
	}
	return commonv1.DownloadSource{}, false
}
