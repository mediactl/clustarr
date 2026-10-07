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

package downloadclient

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/remediation/dlindex"
	cevents "github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/records/recordsource"
	"github.com/mediactl/clustarr/pkg/version"
)

// Engine-record handling (ADR-0019 §6.7).
const (
	// resyncRepublish is how soon a client whose engines have not reached
	// its resyncSeq is reconciled, and its resync republished, again.
	resyncRepublish = 30 * time.Second

	// ReasonProxyUDPUnavailable is the Warning an engine's proxy refusing
	// UDP raises once, on the transition (§7.7).
	ReasonProxyUDPUnavailable = "ProxyUDPUnavailable"
	// ReasonUnidentifiedTransferRemoved records a transfer with no claim
	// removed after its grace, bytes kept (§6.7).
	ReasonUnidentifiedTransferRemoved = "UnidentifiedTransferRemoved"
)

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// engineRecorder is EngineRecorder, else Recorder.
func (r *Reconciler) engineRecorder() k8sevents.EventRecorder {
	if r.EngineRecorder != nil {
		return r.EngineRecorder
	}
	return r.Recorder
}

func (r *Reconciler) books() {
	if r.firstSeen == nil {
		r.firstSeen = map[string]time.Time{}
		r.removalsIssued = map[string]bool{}
		r.ensured = map[string]bool{}
		r.resyncWanted = map[types.NamespacedName]bool{}
	}
}

// engineState is what a client's engine records sum to.
type engineState struct {
	records          map[int32]*schema.EngineRecord
	reported         int
	freshReady       int32
	active           int32
	queued           int32
	seeding          int32
	freeBytes        int64
	unidentified     int32
	proxyUnavailable bool
}

// freeErr is DiskSpaceOK's probe error: no engine reported free bytes yet.
func (e engineState) freeErr() error {
	if e.reported == 0 {
		return fmt.Errorf("no engine has reported its free bytes yet")
	}
	return nil
}

// engineRecords reads each rendered ordinal's engine record: the counts are
// their sums, freeBytes the least free of any engine's scratch and publish
// directories, unidentified their counts' sum, and an engine counts towards
// EngineReady only on a record younger than lifecycle.EngineRecordFresh
// that says ready.
func (r *Reconciler) engineRecords(ctx context.Context, dc *downloadv1alpha1.DownloadClient, replicas int32) (engineState, error) {
	st := engineState{records: map[int32]*schema.EngineRecord{}}
	if r.Engines == nil {
		return st, nil
	}
	now := r.now()
	for o := range max(replicas, 1) {
		rec, _, ok, err := r.Engines.Get(ctx, agentrecords.EngineKey(string(dc.UID), o))
		if err != nil {
			return st, fmt.Errorf("downloadclient: engine record %d: %w", o, err)
		}
		if !ok {
			continue
		}
		st.records[o] = rec
		if rec.Ready && now.Sub(rec.At) <= lifecycle.EngineRecordFresh {
			st.freshReady++
		}
		st.active += rec.Active
		st.queued += rec.Queued
		st.seeding += rec.Seeding
		st.unidentified += max(rec.UnidentifiedCount, int32(len(rec.Unidentified)))
		free := min(rec.ScratchFreeBytes, rec.PublishFreeBytes)
		if rec.ScratchFreeBytes == 0 {
			free = rec.PublishFreeBytes
		}
		if st.reported == 0 || free < st.freeBytes {
			st.freeBytes = free
		}
		st.reported++
		if rec.ProxyUDP == schema.ProxyUDPUnavailable {
			st.proxyUnavailable = true
		}
	}
	return st, nil
}

// setProxyCondition sets ProxyUDPUnavailable from the engine records, with
// one Warning on the transition to True (§7.7).
func (r *Reconciler) setProxyCondition(dc *downloadv1alpha1.DownloadClient, conditions *[]metav1.Condition, unavailable bool) {
	if unavailable {
		if rec := r.engineRecorder(); !k8s.IsConditionTrue(dc.Status.Conditions, downloadv1alpha1.DownloadClientConditionProxyUDPUnavailable) && rec != nil {
			rec.Eventf(dc, nil, "Warning", ReasonProxyUDPUnavailable, "Reconcile",
				"the torrent proxy refuses UDP: the DHT, uTP and UDP trackers are off")
		}
		k8s.MarkTrue(dc, conditions, downloadv1alpha1.DownloadClientConditionProxyUDPUnavailable, ReasonProxyUDPUnavailable,
			"an engine reports its proxy refuses UDP ASSOCIATE")
		return
	}
	k8s.MarkFalse(dc, conditions, downloadv1alpha1.DownloadClientConditionProxyUDPUnavailable, k8s.ReasonReconciled,
		"no engine reports its proxy refusing UDP")
}

// reconcileDurables ensures one durable per rendered ordinal and deletes a
// scaled-away ordinal's once no entry is pinned to its engine (§5.1).
func (r *Reconciler) reconcileDurables(ctx context.Context, dc *downloadv1alpha1.DownloadClient, replicas int32) error {
	if r.Admin == nil {
		return nil
	}
	r.mu.Lock()
	r.books()
	r.mu.Unlock()
	for o := range max(replicas, 1) {
		spec := cevents.EngineConsumer(dc.Name, o)
		r.mu.Lock()
		done := r.ensured[spec.Name]
		r.mu.Unlock()
		if done {
			continue
		}
		if err := r.Admin.EnsureConsumer(ctx, spec); err != nil {
			return fmt.Errorf("downloadclient: ensure durable %s: %w", spec.Name, err)
		}
		r.mu.Lock()
		r.ensured[spec.Name] = true
		r.mu.Unlock()
	}
	_, err := r.sweepDurables(ctx, dc.Namespace, dc.Name, max(replicas, 1))
	return err
}

// sweepDurables deletes client's durables at or past keep (every one when
// keep is 0: the client is gone) once nothing is pinned to their engine,
// and reports how many it kept for a pinned entry.
func (r *Reconciler) sweepDurables(ctx context.Context, namespace, clientName string, keep int32) (int, error) {
	if r.Admin == nil {
		return 0, nil
	}
	durables, err := r.Admin.Subscriptions(ctx, cevents.StreamWorkEngine)
	if err != nil {
		return 0, fmt.Errorf("downloadclient: list engine durables: %w", err)
	}
	kept := 0
	prefix := cevents.EngineConsumerPrefix + cevents.KVKeyToken(clientName) + "-"
	for _, d := range durables {
		rest, ok := strings.CutPrefix(d, prefix)
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(rest, 10, 32)
		if err != nil || int32(n) < keep {
			continue
		}
		engine := lifecycle.EngineName(clientName, int32(n))
		pinned, err := dlindex.PinnedTo(ctx, r.Client, namespace, engine)
		if err != nil {
			return kept, err
		}
		if len(pinned) > 0 {
			kept++
			continue
		}
		if err := r.Admin.DeleteSubscription(ctx, cevents.StreamWorkEngine, d); err != nil {
			return kept, fmt.Errorf("downloadclient: delete durable %s: %w", d, err)
		}
		r.mu.Lock()
		delete(r.ensured, d)
		r.mu.Unlock()
		logging.FromContext(ctx).Info("downloadclient: deleted a scaled-away engine's durable", "durable", d)
	}
	return kept, nil
}

// publishResync publishes the resync command at seq to every engine whose
// record has not reached it; it reports whether one is still pending.
func (r *Reconciler) publishResync(ctx context.Context, dc *downloadv1alpha1.DownloadClient, es engineState, seq int64, now time.Time) bool {
	if seq == 0 || r.Bus == nil {
		return false
	}
	pending := false
	for o := range max(dc.Spec.Replicas, 1) {
		if rec, ok := es.records[o]; ok && rec.ResyncSeq >= seq {
			continue
		}
		pending = true
		cmd := schema.EngineCommand{Desired: schema.EngineDesiredResync, ResyncSeq: seq, IssuedAt: now}
		msgID := cevents.MsgIDForEngineResync(string(dc.UID), o, seq)
		if err := r.publish(ctx, cevents.WorkEngineResyncSubject(dc.Name, o), msgID, dc, cmd, now); err != nil {
			logging.FromContext(ctx).Warn("downloadclient: publish a resync", "ordinal", o, "error", err)
		}
	}
	return pending
}

// removeUnidentified commands the removal, bytes kept, of every transfer an
// engine holds with no claim, once this leader has seen it for
// lifecycle.UnidentifiedGrace and the gate is open (§6.7).
func (r *Reconciler) removeUnidentified(ctx context.Context, dc *downloadv1alpha1.DownloadClient, es engineState, now time.Time) {
	if r.Bus == nil {
		return
	}
	r.mu.Lock()
	r.books()
	r.mu.Unlock()
	gateOpen := r.UnidentifiedGate == nil || r.UnidentifiedGate(ctx, dc)
	for o, rec := range es.records {
		for _, u := range rec.Unidentified {
			key := fmt.Sprintf("%s/%d/%s", dc.UID, o, u.DownloadID)
			r.mu.Lock()
			first, seen := r.firstSeen[key]
			if !seen {
				r.firstSeen[key] = now
				first = now
			}
			issued := r.removalsIssued[key]
			r.mu.Unlock()
			if !gateOpen || issued || now.Sub(first) < lifecycle.UnidentifiedGrace {
				continue
			}
			cmd := schema.EngineCommand{
				Desired: schema.EngineDesiredAbsent, DownloadID: u.DownloadID, RemoveData: false, IssuedAt: now,
			}
			msgID := cevents.MsgIDForEngineRemoveID(string(dc.UID), o, u.DownloadID)
			if err := r.publish(ctx, cevents.WorkEngineIDSubject(dc.Name, o, u.DownloadID), msgID, dc, cmd, now); err != nil {
				logging.FromContext(ctx).Warn("downloadclient: publish an unidentified removal", "downloadID", u.DownloadID, "error", err)
				continue
			}
			r.mu.Lock()
			r.removalsIssued[key] = true
			r.mu.Unlock()
			if rec := r.engineRecorder(); rec != nil {
				rec.Eventf(dc, nil, "Normal", ReasonUnidentifiedTransferRemoved, "Reconcile",
					"engine %s held %q with no claim for %s; removing it, its bytes kept",
					lifecycle.EngineName(dc.Name, o), u.Name, lifecycle.UnidentifiedGrace)
			}
		}
	}
}

// publish puts one engine command on CLUSTARR_WORK_ENGINE.
func (r *Reconciler) publish(ctx context.Context, subject, msgID string, dc *downloadv1alpha1.DownloadClient, cmd schema.EngineCommand, now time.Time) error {
	name, data, err := schema.Encode(cmd)
	if err != nil {
		return err
	}
	env := &cevents.Envelope{
		ID: msgID, Type: "download.EngineCommand", Schema: name, Source: "manager@" + version.String(),
		Key: dc.Namespace + "/" + dc.Name, Time: now, Data: data,
	}
	_, err = r.Bus.Publish(ctx, subject, env, cevents.WithMsgID(msgID), cevents.WithExpectStream(cevents.StreamWorkEngine))
	return err
}

// EngineRecordSource wakes a DownloadClient on its engines' records
// (clustarr-engines; the record's item is the DownloadClient).
func EngineRecordSource(bus cevents.Bus) *recordsource.Source[reconcile.Request] {
	return recordsource.NewItems(bus, cevents.BucketEngines, func(item schema.ItemRef, _ []byte) []reconcile.Request {
		if item.Kind != "DownloadClient" || item.Name == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: item.Namespace, Name: item.Name}}}
	})
}

// transfersRecreated is a records source on clustarr-transfers that decodes
// nothing: its recreation signal (a NATS data loss) marks every
// DownloadClient for a resync and enqueues it (§6.7, "Resync").
func (r *Reconciler) transfersRecreated(c client.Reader) *recordsource.Source[reconcile.Request] {
	none := func(schema.ItemRef, []byte) []reconcile.Request { return nil }
	return recordsource.NewItems(r.Bus, cevents.BucketTransfers, none,
		recordsource.OnRecreated(func(ctx context.Context, enqueue func(reconcile.Request)) {
			var list downloadv1alpha1.DownloadClientList
			if err := c.List(ctx, &list); err != nil {
				logging.FromContext(ctx).Warn("downloadclient: list clients for a resync", "error", err)
				return
			}
			r.mu.Lock()
			r.books()
			for i := range list.Items {
				r.resyncWanted[client.ObjectKeyFromObject(&list.Items[i])] = true
			}
			r.mu.Unlock()
			for i := range list.Items {
				enqueue(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
			}
		}),
		recordsource.WithItemDecoder[reconcile.Request](func([]byte) (schema.ItemRef, string, bool) {
			return schema.ItemRef{}, "", false
		}))
}
