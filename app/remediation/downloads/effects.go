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

package downloads

import (
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/version"
)

// source is every envelope's Source.
func source() string { return "manager@" + version.String() }

// blockCall is one owed blocklist call (R9): its reply goes into the block
// book, which the next pass reads to confirm the block or record the
// unblock's nonce.
func (s *Stage) blockCall(b lifecycle.BlockCall) remediation.Effect {
	reply := &schema.BlocklistResponse{}
	op := b.Req.Op
	return remediation.RPC{
		Subject: events.RPCIndexBlocklist, Request: b.Req, Reply: reply, Timeout: lifecycle.BlockCallTimeout,
		Done: func(_ any, err error) {
			switch {
			case err != nil:
				metrics.BlocklistCallsTotal.WithLabelValues(op, "error").Inc()
				return
			case reply.Error != "":
				metrics.BlocklistCallsTotal.WithLabelValues(op, "error").Inc()
				return
			case reply.Applied:
				metrics.BlocklistCallsTotal.WithLabelValues(op, "applied").Inc()
			default:
				metrics.BlocklistCallsTotal.WithLabelValues(op, "stale").Inc()
			}
			s.blocks.record(b.EntryUID, b.Nonce, lifecycle.BlockReply{
				Seq: b.Req.Seq, Applied: reply.Applied, Stale: reply.Stale, At: s.o.Clock().UTC(),
			})
		},
	}
}

// command renders one engine command as a Publish on CLUSTARR_WORK_ENGINE
// (§6.7): subject <client>.<ordinal>.<entry uid>, Msg-Id
// engine/<entry uid>/<seq>[/<bootID>]. Engine commands are not admitted
// through the ledger (§5.1: engine readiness gates them).
func (s *Stage) command(ow owner, c lifecycle.Command, now metav1.Time) (remediation.Effect, bool) {
	if c.Client == "" || c.Cmd.Entry.UID == "" {
		return nil, false
	}
	msgID := events.MsgIDForEngineCommand(c.Cmd.Entry.UID, c.Cmd.Seq)
	if c.BootID != "" {
		msgID = events.MsgIDForEngineCommandAfterBoot(c.Cmd.Entry.UID, c.Cmd.Seq, c.BootID)
	}
	name, data, err := schema.Encode(c.Cmd)
	if err != nil {
		return nil, false
	}
	outcome := "published"
	if !c.Cmd.IssuedAt.IsZero() && c.Cmd.IssuedAt.Before(now.Time) {
		outcome = "republished"
	}
	metrics.EngineCommandsTotal.WithLabelValues(c.Cmd.Desired, outcome).Inc()
	return remediation.Publish{
		Subject:      events.WorkEngineSubject(c.Client, c.Ordinal, c.Cmd.Entry.UID),
		MsgID:        msgID,
		ExpectStream: events.StreamWorkEngine,
		Envelope: &events.Envelope{
			ID: msgID, Type: "download.EngineCommand", Schema: name, Source: source(),
			Key: ow.ref.Namespace + "/" + ow.ref.Name, Time: now.UTC(), Data: data,
		},
	}, true
}

// history publishes one DownloadEvent on clustarr.evt.download.download
// .<action>.<entry uid>, Msg-Id <entry uid>:<action>:<seq>, for the history
// sink.
func (s *Stage) history(ow owner, v lifecycle.View, plan lifecycle.Plan, h schema.DownloadEvent) (remediation.Effect, bool) {
	uid := h.DownloadRef.UID
	if uid == "" {
		return nil, false
	}
	seq := entrySeq(plan.Entries, uid)
	if seq == 0 {
		seq = entrySeq(v.Stored, uid)
	}
	name, data, err := schema.Encode(h)
	if err != nil {
		return nil, false
	}
	id := uid + ":" + h.Action + ":" + strconv.FormatInt(seq, 10)
	return remediation.History{
		Subject: events.DownloadEventSubject(h.Action, uid),
		MsgID:   id,
		Envelope: &events.Envelope{
			ID: id, Type: "download.DownloadEvent", Schema: name, Source: source(),
			Key: ow.ref.Namespace + "/" + h.DownloadRef.Name, Time: h.At, Data: data,
		},
	}, true
}

func entrySeq(entries []catalogv1alpha1.DownloadEntry, uid string) int64 {
	for i := range entries {
		if entries[i].UID == uid {
			return entries[i].Dispatch.Seq
		}
	}
	return 0
}

// event renders a lifecycle Event: on the owner, or on the DownloadClient
// it names (TransferOwnerGone).
func (s *Stage) event(ow owner, e lifecycle.Event) remediation.ItemEvent {
	ie := remediation.ItemEvent{Recorder: e.Recorder, Type: e.Type, Reason: e.Reason, Message: e.Message}
	if e.On != nil && e.On.Kind == "DownloadClient" {
		ie.On = &downloadv1alpha1.DownloadClient{ObjectMeta: metav1.ObjectMeta{
			Namespace: e.On.Namespace, Name: e.On.Name, UID: types.UID(e.On.UID),
		}}
	}
	if ie.On == nil && ie.Recorder == "" {
		ie.Recorder = lifecycle.RecorderDownloads
	}
	return ie
}

// unclaimedAbsent is the owner-gone removal of one unclaimed transfer: an
// absent command at the seq after its record's, with the claim's data rule.
func unclaimedAbsent(t lifecycle.Transfer, now time.Time) (remediation.Effect, bool) {
	rec := t.Record
	if rec.Claim == nil {
		return nil, false
	}
	client, ordinal, ok := splitEngine(rec.Engine)
	if !ok {
		return nil, false
	}
	cmd := schema.EngineCommand{
		Seq: rec.Seq + 1, Owner: rec.Claim.Owner, Entry: rec.Claim.Entry, Desired: schema.EngineDesiredAbsent,
		RemoveData: rec.Claim.RemoveDataOnDelete, Claim: *rec.Claim, IssuedAt: now,
		Release: schema.CommandRelease{Title: rec.Claim.Title, GUID: rec.Claim.GUID, IndexerRef: rec.Claim.Indexer},
	}
	name, data, err := schema.Encode(cmd)
	if err != nil {
		return nil, false
	}
	msgID := events.MsgIDForEngineCommand(cmd.Entry.UID, cmd.Seq)
	metrics.EngineCommandsTotal.WithLabelValues(cmd.Desired, "published").Inc()
	return remediation.Publish{
		Subject: events.WorkEngineSubject(client, ordinal, cmd.Entry.UID), MsgID: msgID, ExpectStream: events.StreamWorkEngine,
		Envelope: &events.Envelope{
			ID: msgID, Type: "download.EngineCommand", Schema: name, Source: source(),
			Key: rec.Claim.Owner.Namespace + "/" + rec.Claim.Owner.Name, Time: now, Data: data,
		},
	}, true
}

// splitEngine reads "<client>-<ordinal>", splitting at the last hyphen.
func splitEngine(engine string) (string, int32, bool) {
	for i := len(engine) - 1; i > 0; i-- {
		if engine[i] != '-' {
			continue
		}
		n, err := strconv.ParseInt(engine[i+1:], 10, 32)
		if err != nil || n < 0 {
			return "", 0, false
		}
		return engine[:i], int32(n), true
	}
	return "", 0, false
}

// downloadClientRef names the DownloadClient an engine belongs to, for an
// Event.
func downloadClientRef(namespace, engine string) *schema.ItemRef {
	client, _, ok := splitEngine(engine)
	if !ok {
		return nil
	}
	return &schema.ItemRef{Kind: "DownloadClient", Ref: schema.Ref{Namespace: namespace, Name: client}}
}
