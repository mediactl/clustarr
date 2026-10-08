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

package searchplan

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/wantedcron"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Durable is the consumer every automatic search is admitted to: the
// normal and low lanes of catalog-search-normal.
const Durable = events.ConsumerCatalogSearchNorm

// Timing of a search dispatch.
const (
	// Timeout closes a dispatch no record answered, without counting an
	// attempt: catalog-search-normal's AckWait × MaxDeliver plus 5 min.
	Timeout = 15 * time.Minute
	// RepublishEvery is how often an unanswered dispatch is republished
	// under its Msg-Id (inside the work stream's one-hour duplicate
	// window, so a task already published is absorbed).
	RepublishEvery = 2 * time.Minute
	// DonorSearchesPerSweep caps the first-time live donor searches per
	// namespace per wantedcron period (P4, §7.3).
	DonorSearchesPerSweep = 20
)

// View is everything Decide reads for one searchable item.
type View struct {
	Item      schema.ItemRef
	Container *schema.ItemRef
	Kind      commonv1.MediaKind
	Now       time.Time
	// Monitored: an Episode needs its Series monitored too (P2).
	Monitored bool
	// Searchable: the kind can be grabbed (P1).
	Searchable bool
	// Wanted: missing or below its cutoff (wantedcron.Candidate's rule);
	// Reason says which.
	Wanted bool
	Reason schema.SearchReason
	// Profile is the resolved QualityProfile's name; "" dispatches nothing
	// (P3).
	Profile    string
	Protocols  []commonv1.Protocol
	Priorities map[string]int32
	// Scope is the release index's block scope of the item's grab owner.
	Scope    string
	MediaRef commonv1.MediaRef
	Dispatch *catalogv1alpha1.Dispatch
	// Record is the record at the item's key, any seq.
	Record        *schema.SearchRecord
	LastSearched  *metav1.Time
	Attempts      commonv1.Attempts
	DonorAttempts commonv1.Attempts
	// Donor is what a donor search must carry (P4); nil when no donor is
	// wanted.
	Donor *schema.DonorWant
	// DonorBudget: the namespace's first-time live donor budget has room.
	DonorBudget bool
	// Redownload is the failure time of a covering Failed or Blocklisted
	// entry asking a search (R25).
	Redownload *time.Time
	// InFlight is the task the stage last published for the dispatch in
	// flight, and when; nil after a leader change (the task is rebuilt).
	InFlight *Task
	// Key is the dispatch's ledger key.
	Key dispatch.Key
	// SeasonPending: an Episode's season is Queued or Searching in its
	// Series' season search (Wave S, §7): its own video search waits for it,
	// so a season turned on and searched at once is one query, not one per
	// episode. A redownload does not wait.
	SeasonPending bool
	// SeasonAnsweredAt is when its Series' season search answered the
	// Episode's season (Answered, not AllPaced): a season answer newer than
	// the Episode's lastSearchedAt counts once as its search (§7).
	SeasonAnsweredAt *metav1.Time
}

// Task is one search task to publish.
type Task struct {
	Subject, MsgID string
	Payload        schema.SearchTask
	// Live reserves the namespace's pacing class; FirstLiveDonor spends
	// the donor budget.
	Live, FirstLiveDonor bool
	// Republish marks a republish of the dispatch in flight.
	Republish bool
	At        time.Time
}

// Plan is Decide's answer: catalog-grab's bookkeeping, the task owed and
// when to look again.
type Plan struct {
	Dispatch       *catalogv1alpha1.Dispatch
	Publish        *Task
	LastSearchedAt *metav1.Time
	Attempts       commonv1.Attempts
	DonorAttempts  commonv1.Attempts
	// Answered: the dispatch at AnsweredSeq was closed this pass (an
	// incorporated record, or a timeout), which the ledger is told.
	Answered    bool
	AnsweredSeq int64
	// Waiting is the reason a due search was refused this pass ("" for
	// none), which the ledger counts.
	Waiting dispatch.Reason
	Due     time.Time
}

// Decide is the item key's search plan (§7.3). admit asks the dispatch
// ledger for a slot on durable at seq; pace reserves the item's slot in the
// namespace's live-search pacing class and returns it.
func Decide(v View, admit func(durable string, seq int64) dispatch.Decision, pace func() time.Time) Plan {
	now := v.Now.UTC()
	p := Plan{
		Dispatch: v.Dispatch.DeepCopy(), LastSearchedAt: v.LastSearched,
		Attempts: v.Attempts, DonorAttempts: v.DonorAttempts,
	}
	d := p.Dispatch

	// 1. Incorporate the record that answers the dispatch in flight.
	if d.InFlight() && v.Record != nil && v.Record.Seq == d.Seq {
		at := metav1.NewTime(v.Record.FinishedAt.UTC().Truncate(time.Second))
		if at.IsZero() {
			at = metav1.NewTime(now.Truncate(time.Second))
		}
		d.AnsweredSeq, d.Withdrawn, d.Delivery = d.Seq, false, nil
		donor := v.Record.Purpose == schema.SearchPurposeAudioDonor
		if !v.Record.AllPaced {
			// A search no indexer was asked is not an attempt (P6).
			if donor {
				p.DonorAttempts = bump(p.DonorAttempts, at)
			} else {
				p.Attempts = bump(p.Attempts, at)
			}
		}
		if !donor {
			p.LastSearchedAt = &at
		}
		p.Answered, p.AnsweredSeq = true, d.Seq
	}

	// 1b. A season search's answer counts as the Episode's search (Wave S,
	// §7): folded once, while it is wanted and nothing of its own is in
	// flight, never moving lastSearchedAt back. Its next search is then
	// IndexOnly against the release index the season query fed.
	if at := v.SeasonAnsweredAt; at != nil && v.Wanted && !d.InFlight() &&
		(p.LastSearchedAt == nil || at.After(p.LastSearchedAt.Time)) {
		t := *at
		p.LastSearchedAt = &t
		p.Attempts = bump(p.Attempts, t)
	}
	// Due-ness is decided on the bookkeeping this pass writes: a fold is
	// the Episode's search, so its backoff starts at the season's answer and
	// its next search is IndexOnly -- not a live search in the same pass.
	v.Attempts, v.LastSearched = p.Attempts, p.LastSearchedAt

	// 2. A dispatch nobody answered closes as timed out, uncounted.
	if d.InFlight() && now.Sub(d.DispatchedAt.Time) >= Timeout {
		d.AnsweredSeq, d.Withdrawn, d.Delivery = d.Seq, true, nil
		p.Answered, p.AnsweredSeq = true, d.Seq
	}

	// 3. In flight: republish an unanswered task now and then.
	if d.InFlight() {
		p.Due = d.DispatchedAt.Add(Timeout)
		last := time.Time{}
		if v.InFlight != nil {
			last = v.InFlight.At
		}
		if last.IsZero() || now.Sub(last) >= RepublishEvery {
			var t *Task
			if v.InFlight != nil {
				cp := *v.InFlight
				t = &cp
			} else {
				t = task(v, d.Seq, purposeDue(v, now), now)
			}
			t.Republish, t.At = true, now
			p.Publish = t
			last = now
		}
		if next := last.Add(RepublishEvery); next.Before(p.Due) {
			p.Due = next
		}
		dispatch.Render(d, Durable, nil, nil, v.Key)
		return p
	}
	if d != nil {
		d.Delivery = nil
	}

	// 4. Due?
	purpose := purposeDue(v, now)
	if purpose == nil {
		return p
	}
	t := task(v, records(v, d, now), purpose, now)

	// 5. A live search waits its pace (§5.4); an index-only one is not
	// paced.
	if t.Live {
		if slot := pace(); slot.After(now.Add(time.Second)) {
			p.Waiting, p.Due = catalogv1alpha1.DeliveryReasonPaced, slot
			waiting(&p, dispatch.Decision{
				Reason:  catalogv1alpha1.DeliveryReasonPaced,
				Message: "waiting for the namespace's live-search pace",
			}, v.Key)
			return p
		}
	}

	// 6. Admission.
	dec := admit(Durable, t.Payload.Seq)
	if !dec.Admitted {
		p.Waiting = dec.Reason
		p.Due = now.Add(max(dec.RetryAfter, time.Second))
		waiting(&p, dec, v.Key)
		return p
	}
	prev := int64(0)
	if d != nil {
		prev = d.AnsweredSeq
	}
	nd := &catalogv1alpha1.Dispatch{
		Seq: t.Payload.Seq, AnsweredSeq: prev, DispatchedAt: metav1.NewTime(now.Truncate(time.Second)),
	}
	dispatch.Render(nd, Durable, &dec, nil, v.Key)
	p.Dispatch, p.Publish = nd, t
	p.Due = now.Add(RepublishEvery)
	return p
}

// waiting renders a refused dispatch on the stored block, which keeps its
// seq (a block that never dispatched has no seq to show it on).
func waiting(p *Plan, dec dispatch.Decision, k dispatch.Key) {
	if p.Dispatch == nil {
		return
	}
	dispatch.Render(p.Dispatch, "", &dec, nil, k)
}

// purposeDue is the search the item is due, nil for none: the video's
// (wanted on its backoff, or a redownload at once, R25), else a donor's on
// its own backoff, a first-time live one only with budget (P4).
func purposeDue(v View, now time.Time) *purpose {
	if !v.Searchable || !v.Monitored || v.Profile == "" {
		return nil
	}
	if redownloadDue(v, now) {
		return &purpose{reason: schema.SearchReasonRedownload, live: true}
	}
	// A dispatch that closed without counting -- every indexer paced (P6),
	// or nobody answered -- still waits the backoff's floor before the next,
	// as the item waited for the next sweep before: a paced pool must not
	// come straight back at the index agent's limiter.
	if d := v.Dispatch; d != nil && now.Sub(d.DispatchedAt.Time) < wantedcron.MinimumGap {
		return nil
	}
	// An Episode whose season search is queued or searching waits for it
	// (Wave S, §7); its donor search does not.
	if v.Wanted && !v.SeasonPending && wantedcron.Eligible(v.Attempts, now) {
		reason := v.Reason
		if reason == "" {
			reason = schema.SearchReasonMissing
		}
		return &purpose{reason: reason, live: !searched(v.Attempts, v.LastSearched)}
	}
	if v.Donor != nil && wantedcron.Eligible(v.DonorAttempts, now) {
		first := !searched(v.DonorAttempts, nil)
		if first && !v.DonorBudget {
			return nil
		}
		return &purpose{reason: schema.SearchReasonMissing, donor: true, live: first}
	}
	return nil
}

type purpose struct {
	reason      schema.SearchReason
	donor, live bool
}

// redownloadDue: a covering failure newer than the last search dispatch and
// younger than RedownloadWindow asks a search at once (R25).
func redownloadDue(v View, now time.Time) bool {
	if v.Redownload == nil || now.Sub(*v.Redownload) >= lifecycle.RedownloadWindow {
		return false
	}
	return v.Dispatch == nil || v.Dispatch.DispatchedAt.Time.Before(*v.Redownload)
}

// searched reports an item searched before (P12): its next search is
// IndexOnly.
func searched(a commonv1.Attempts, last *metav1.Time) bool {
	return a.Count > 0 || a.Latest != nil || last != nil
}

// records is the next seq: records.NextSeq over the record's and the
// status' seqs.
func records(v View, d *catalogv1alpha1.Dispatch, now time.Time) int64 {
	var rs, ss int64
	if v.Record != nil {
		rs = v.Record.Seq
	}
	if d != nil {
		ss = d.Seq
	}
	return max(rs+1, ss+1, now.UnixMilli())
}

// task is the SearchTask v2 for seq (§7.3): normal lane for a live search,
// low for an index-only one, Msg-Id events.MsgIDForSearch(item uid, seq).
func task(v View, seq int64, pu *purpose, now time.Time) *Task {
	if pu == nil {
		pu = &purpose{reason: schema.SearchReasonMissing}
	}
	payload := schema.SearchTask{
		MediaRef: v.MediaRef, Reason: pu.reason, IndexOnly: !pu.live,
		Item: v.Item, Container: v.Container, Seq: seq,
		QualityProfile: v.Profile, Protocols: v.Protocols, IndexerPriorities: v.Priorities, Scope: v.Scope,
	}
	if pu.donor {
		payload.Purpose = schema.SearchPurposeAudioDonor
		payload.Donor = v.Donor
	}
	lane := events.PriorityNormal
	if payload.IndexOnly {
		lane = events.PriorityLow
	}
	mediaKey := events.MediaKey(string(v.MediaRef.Kind), v.Item.Namespace, v.Item.Name)
	return &Task{
		Subject: events.WorkSearchSubject(lane, mediaKey),
		MsgID:   events.MsgIDForSearch(v.Item.UID, seq),
		Payload: payload, Live: pu.live, FirstLiveDonor: pu.donor && pu.live, At: now,
	}
}

// bump counts one attempt at at.
func bump(a commonv1.Attempts, at metav1.Time) commonv1.Attempts {
	if a.Initial == nil {
		a.Initial = &at
	}
	a.Latest = &at
	a.Count++
	return a
}
