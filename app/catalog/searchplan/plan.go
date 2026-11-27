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
	"github.com/mediactl/clustarr/app/catalog/itemread"
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
	// PostReleaseDelay is how long after its release date an item still
	// wanted gets one more live search (owner, 2026-11-17). A provider
	// gives an air or release date as a day, read as 00:00 UTC, so the
	// item's one live search runs up to a day before the episode airs and
	// finds nothing; after it only the RSS feeds and the release index were
	// asked, and a release they missed waited for a BacklogSearch's cooldown.
	// 36 hours is noon UTC of the next day: past a US late-night broadcast
	// of the date.
	PostReleaseDelay = 36 * time.Hour
	// PostReleaseWindow bounds that search to items released lately: past
	// it the item is the backlog's, on its cooldown.
	PostReleaseWindow = 7 * 24 * time.Hour
	// RedownloadRetry is the least a redownload waits after a dispatch
	// that asked no indexer -- every one paced, none to ask, or nobody
	// answered -- before it is dispatched again (owner, 2026-11-17: "The
	// retry should enqueue a release search ..."; such a dispatch had
	// counted as the failure's search, so the retry ended there). The wait
	// grows with the time since the failure, to RedownloadRetryMax, so an
	// indexer outage does not fill the namespace's live-search pace.
	RedownloadRetry    = 5 * time.Minute
	RedownloadRetryMax = time.Hour
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
	// HasFile: the item holds a file (status.hasFile); with Wanted false it
	// wants nothing, and owes no redownload.
	HasFile bool
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
	// Episode's season (Answered, not AllPaced or NoIndexer): a season
	// answer newer than the Episode's lastSearchedAt counts once as its
	// search (§7).
	SeasonAnsweredAt *metav1.Time
	// SeasonPackFirst: an Episode's season passes PackFirst in its Series'
	// status.seasons (owner, 2026-10-27). Until the Episode has been
	// searched at all, its video search waits for the automatic season
	// search, which asks for every such season with an episode waiting
	// (automaticSeasons), so a pack is taken before a single could be; the
	// season's answer is then its search, folded as SeasonAnsweredAt.
	SeasonPackFirst bool
	// SeasonReplaces: the Episode and another of its season hold files a
	// whole release replaces (undecodable video, a wanted audio language
	// missing), both due, and no grab holds the season (owner, 2026-11-03).
	// Its video search waits for the automatic season search, which asks
	// for such a season (SeasonFacts.ReplaceDue); the answer is its search.
	SeasonReplaces bool
	// Backlog: a BacklogSearch cycle granted the item one live search
	// (app/catalog/backlog; backlog search design). Its video search, while
	// wanted, is live and due at once, whatever its backoff, unless it
	// waits for its season.
	Backlog bool
	// SeasonBacklog: an Episode with a backlog grant whose season the
	// Series' automatic season search asks for (SeasonFacts.BacklogDue,
	// two or more granted episodes, no grab holding the season). It waits
	// for the season, whose answer is its search.
	SeasonBacklog bool
	// LastLiveSearch is status.lastLiveSearchAt.
	LastLiveSearch *metav1.Time
	// ReleasedAt is the day the item came out, as its provider dates it: an
	// Episode's air date, a Movie's availability date. Nil for an item with
	// none, which gets no post-release search (PostReleaseDelay).
	ReleasedAt *time.Time
	// NoSearchOnAdd: the item was added with searchForMissing false (a
	// Movie's searchForMovie; a Series', Comic's, Artist's or Author's for a
	// child that was already out when the container was added; 2026-11-10
	// design D10, D11) -- Sonarr's "search for missing episodes" unticked.
	// While the item has never been searched its automatic search asks the
	// release index alone, never the indexers, and it does not wait for
	// its season's or volume's automatic pack search, which is not asked
	// for it; a backlog grant, a redownload and a person's search are live
	// as ever, and once searched the flag means nothing. The zero value is
	// the default, search on add.
	NoSearchOnAdd bool
}

// Task is one search task to publish.
type Task struct {
	Subject, MsgID string
	Payload        schema.SearchTask
	// Live reserves the namespace's pacing class; FirstLiveDonor spends
	// the donor budget; Backlog spends the item's backlog grant.
	Live, FirstLiveDonor, Backlog bool
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
	// LastLiveSearchAt is status.lastLiveSearchAt: moved by a live video
	// dispatch and by a season answer folded in.
	LastLiveSearchAt *metav1.Time
	Attempts         commonv1.Attempts
	DonorAttempts    commonv1.Attempts
	// SeasonFolded: a season search's answer was folded in as the item's
	// search this pass, which spends a backlog grant waiting for it.
	SeasonFolded bool
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
		Dispatch: v.Dispatch.DeepCopy(), LastSearchedAt: v.LastSearched, LastLiveSearchAt: v.LastLiveSearch,
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
		if !v.Record.AllPaced && !v.Record.NoIndexer {
			// A search no indexer was asked -- every one paced, or none
			// to ask (no Indexer yet, or every one disabled, unprobed or in
			// backoff: smoke finding 7) -- is not an attempt (P6), nor a
			// search: lastSearchedAt is read as one (wantedcron folds it
			// into the attempts), and an item whose only live search asked
			// no indexer must still get its one live search (P12).
			if donor {
				p.DonorAttempts = bump(p.DonorAttempts, at)
			} else {
				p.Attempts = bump(p.Attempts, at)
				p.LastSearchedAt = &at
			}
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
		// A season search asks the indexers: it is the Episode's live search
		// too, and a backlog grant waiting for it is spent.
		if p.LastLiveSearchAt == nil || t.After(p.LastLiveSearchAt.Time) {
			live := t
			p.LastLiveSearchAt = &live
		}
		p.SeasonFolded = true
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
	// Due-ness reads the dispatch as this pass leaves it: one closed above
	// is no longer in flight.
	v.Dispatch = d

	// 4. Due?
	purpose := purposeDue(v, now)
	if purpose == nil {
		// A redownload whose dispatch asked no indexer is owed again, and
		// nothing wakes the item for it but this.
		if at, ok := redownloadRetryAt(v, now); ok && at.After(now) {
			p.Due = at
		}
		// Nothing wakes the item for its post-release search but this.
		if at, ok := postRelease(v, now); ok {
			if d != nil {
				if floor := d.DispatchedAt.Add(wantedcron.MinimumGap); floor.After(at) {
					at = floor
				}
			}
			// Only a moment still ahead: one already past is held by the
			// item's season, whose answer wakes it, and a Due in the past
			// would bring the pass straight back.
			if at.After(now) && (p.Due.IsZero() || at.Before(p.Due)) {
				p.Due = at
			}
		}
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
	if t.Live && t.Payload.Purpose == "" {
		// A live video search: a BacklogSearch's cooldown runs from here (a
		// record does not say whether its search was live).
		at := nd.DispatchedAt
		p.LastLiveSearchAt = &at
	}
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
	if RedownloadDue(v, now) {
		return &purpose{reason: schema.SearchReasonRedownload, live: true}
	}
	// An Episode whose season search is queued or searching waits for it
	// (Wave S, §7), one never searched waits for its PackFirst season's
	// automatic search -- unless it was added unsearched, since that search
	// is not asked for it -- and one replacing or granted with its season
	// waits for that season's; its donor search does not.
	first := !searched(v.Attempts, v.LastSearched)
	waits := v.SeasonPending || (v.SeasonPackFirst && first && !v.NoSearchOnAdd) || v.SeasonReplaces || v.SeasonBacklog
	// A backlog grant (backlog search design): one live search, now. The
	// BacklogSearch's cooldown and hourly limit stand in for the backoff,
	// and the live-search pace and the ledger still apply.
	if v.Backlog && v.Wanted && !waits {
		reason := v.Reason
		if reason == "" {
			reason = schema.SearchReasonMissing
		}
		return &purpose{reason: reason, live: true, backlog: true}
	}
	// A dispatch that closed without counting -- every indexer paced (P6),
	// or nobody answered -- still waits the backoff's floor before the next,
	// as the item waited for the next sweep before: a paced pool must not
	// come straight back at the index agent's limiter.
	if d := v.Dispatch; d != nil && now.Sub(d.DispatchedAt.Time) < wantedcron.MinimumGap {
		return nil
	}
	// The post-release search (PostReleaseDelay): live, once, whatever the
	// backoff, behind the pace and the ledger as every live search is.
	if at, ok := postRelease(v, now); ok && !waits && !now.Before(at) {
		reason := v.Reason
		if reason == "" {
			reason = schema.SearchReasonMissing
		}
		return &purpose{reason: reason, live: true}
	}
	if v.Wanted && !waits && wantedcron.Eligible(v.Attempts, now) {
		reason := v.Reason
		if reason == "" {
			reason = schema.SearchReasonMissing
		}
		// An item's first search is live (P12), unless it was added with
		// searchForMissing false: then the release index alone answers it,
		// as it does every later one.
		return &purpose{reason: reason, live: first && !v.NoSearchOnAdd}
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
	reason               schema.SearchReason
	donor, live, backlog bool
}

// postRelease is when the item's post-release live search is owed
// (PostReleaseDelay after its release date), and whether one is: only a
// wanted item released within PostReleaseWindow whose last live search was
// before that moment, and never one added with searchForMissing false.
func postRelease(v View, now time.Time) (time.Time, bool) {
	if v.ReleasedAt == nil || v.ReleasedAt.IsZero() || !v.Wanted || v.NoSearchOnAdd {
		return time.Time{}, false
	}
	if !v.Searchable || !v.Monitored || v.Profile == "" {
		return time.Time{}, false
	}
	at := v.ReleasedAt.Add(PostReleaseDelay)
	if !now.Before(v.ReleasedAt.Add(PostReleaseWindow)) {
		return time.Time{}, false
	}
	if v.LastLiveSearch != nil && !v.LastLiveSearch.Time.Before(at) {
		return time.Time{}, false
	}
	return at, true
}

// RedownloadDue: a covering failure newer than the last search dispatch and
// younger than RedownloadWindow asks a search at once (R25), unless the item
// holds a file and wants nothing (Lazarus S01E02, 2026-11-02: a file was
// imported and transcoded after its grab failed, and its redownload was
// still owed as a live search). It reads the stored phase, so a pass before
// the item's own apply still owes it. The stage admits such a search
// through dispatch.Ledger.AdmitFirst.
func RedownloadDue(v View, now time.Time) bool {
	if v.Redownload == nil || now.Sub(*v.Redownload) >= lifecycle.RedownloadWindow {
		return false
	}
	if v.HasFile && !v.Wanted {
		return false
	}
	if !v.Searchable || !v.Monitored || v.Profile == "" {
		return false
	}
	if v.Dispatch == nil || v.Dispatch.DispatchedAt.Time.Before(*v.Redownload) {
		return true
	}
	// Dispatched since the failure. A dispatch that asked no indexer was no
	// search (itemread.SearchCounted): the redownload is still owed, once
	// its retry wait has passed.
	at, ok := redownloadRetryAt(v, now)
	return ok && !now.Before(at)
}

// redownloadRetryAt is when a redownload dispatched since its failure and
// closed without asking an indexer is dispatched again, and whether one is
// owed so: RedownloadRetry after that dispatch, or as long after it as it
// was after the failure, to RedownloadRetryMax.
func redownloadRetryAt(v View, now time.Time) (time.Time, bool) {
	d := v.Dispatch
	if v.Redownload == nil || d == nil || d.InFlight() || d.DispatchedAt.Time.Before(*v.Redownload) ||
		now.Sub(*v.Redownload) >= lifecycle.RedownloadWindow {
		return time.Time{}, false
	}
	if (v.HasFile && !v.Wanted) || !v.Searchable || !v.Monitored || v.Profile == "" {
		return time.Time{}, false
	}
	if itemread.SearchCounted(d, v.LastSearched) {
		return time.Time{}, false
	}
	wait := min(max(d.DispatchedAt.Sub(*v.Redownload), RedownloadRetry), RedownloadRetryMax)
	return d.DispatchedAt.Add(wait), true
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
		Payload: payload, Live: pu.live, FirstLiveDonor: pu.donor && pu.live, Backlog: pu.backlog, At: now,
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
