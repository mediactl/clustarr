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
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	recordstate "github.com/mediactl/clustarr/pkg/records"
)

// SeasonDurable is the consumer every season task is admitted to: a person
// is waiting, so the high lane (series search design §4.5).
const SeasonDurable = events.ConsumerCatalogSearchHigh

// SeriesSearchStallTimeout fails a planned Search whose served state shows
// no transition for this long (the Search controller reads it, §4.6).
const SeriesSearchStallTimeout = 30 * time.Minute

// maxSeasonAttempts is a season's dispatch cap: a season every indexer
// paced goes once more to the back of the queue, then counts as answered.
const maxSeasonAttempts = 2

// SeasonRequest is one open planned Search naming the series.
type SeasonRequest struct {
	Search catalogv1alpha1.SeasonSearchRequest
	// Season is spec.season; nil asks every season but the specials.
	Season   *int32
	Created  time.Time
	Deleting bool
}

// SeasonFacts is one season of the series as the planner reads it.
type SeasonFacts struct {
	Number int32
	// Monitored: the series and at least one episode of the season.
	Monitored bool
	// Wanted counts the monitored, aired episodes missing or below their
	// cutoff, or covered by a live grab.
	Wanted int
	// Downloading counts the Wanted ones a live entry covers.
	Downloading int
}

// SeasonView is everything DecideSeasons reads for one Series.
type SeasonView struct {
	Series schema.ItemRef
	Now    time.Time
	// Monitored is the series' own spec.monitored.
	Monitored  bool
	Anime      bool
	Profile    string
	Protocols  []commonv1.Protocol
	Priorities map[string]int32
	Scope      string
	// State is the stored status.seasonSearch.
	State *catalogv1alpha1.SeasonSearchState
	// Requests are the open planned Searches (not Completed or Failed),
	// oldest first.
	Requests []SeasonRequest
	// ServedSettled: the state's Search reads Completed or Failed, or is
	// gone (the handshake, §4.3).
	ServedSettled bool
	// ServedGone: the state's Search is gone or deleting.
	ServedGone bool
	// Seasons are the series' seasons (status.seasons), with their facts.
	Seasons []SeasonFacts
	// Records are the records at each season's key, any seq.
	Records      map[int32]*schema.SearchRecord
	LastSearched *metav1.Time
	// InFlight is the task the stage last published for the dispatch in
	// flight; nil after a leader change (the task is rebuilt).
	InFlight *Task
	// Key is the season dispatch's ledger key.
	Key dispatch.Key
}

// SeasonPlan is DecideSeasons' answer: catalog-grab's seasonSearch and
// lastSearchedAt, the task owed and when to look again.
type SeasonPlan struct {
	State          *catalogv1alpha1.SeasonSearchState
	LastSearchedAt *metav1.Time
	Publish        *Task
	// Answered: the dispatch at AnsweredSeq was closed this pass, which the
	// ledger is told.
	Answered    bool
	AnsweredSeq int64
	// Waiting is the reason a due season was refused this pass.
	Waiting dispatch.Reason
	Due     time.Time
	// Started: a request was taken this pass; Finals: the seasons that
	// reached a final phase this pass (the metric and the Events), each
	// with the request it served.
	Started bool
	Finals  []SeasonFinal
}

// SeasonFinal is one season that reached a final phase in a pass, and the
// request it served.
type SeasonFinal struct {
	Request catalogv1alpha1.SeasonSearchRequest
	Season  catalogv1alpha1.SeasonSearchSeason
}

// DecideSeasons is the Series key's season plan (series search design
// §4.3, §4.4): it serves one planned Search at a time from
// status.seasonSearch, with exactly one season task in flight, each behind
// a slot of the namespace's live-search pace (pace reserves the season's)
// and admitted through the dispatch ledger on SeasonDurable (admit). It
// publishes no Episode search: the episodes a season's answer leaves
// uncovered are their own planners' (§7).
func DecideSeasons(v SeasonView, admit func(durable string, seq int64) dispatch.Decision, pace func(season int32) time.Time) SeasonPlan {
	now := v.Now.UTC()
	p := SeasonPlan{State: v.State.DeepCopy(), LastSearchedAt: v.LastSearched}
	before := phasesOf(v.State)

	// 1. Canceling: the served Search is gone before its state finished.
	if st := p.State; st != nil && st.FinishedAt == nil && v.ServedGone {
		if d := st.Dispatch; d.InFlight() {
			d.AnsweredSeq, d.Withdrawn, d.Delivery = d.Seq, true, nil
			p.Answered, p.AnsweredSeq = true, d.Seq
		}
		for i := range st.Seasons {
			if s := &st.Seasons[i]; !s.Phase.Final() {
				s.Phase, s.Reason = catalogv1alpha1.SeasonSearchCanceled, catalogv1alpha1.SeasonSearchSearchDeleted
			}
		}
		finish(&p, now)
	}

	// 2. Taking a request: no state, or a finished one whose Search has
	// copied its outcome (the handshake) or is gone.
	if st := p.State; st == nil || (st.FinishedAt != nil && (v.ServedSettled || v.ServedGone)) {
		if req, ok := nextRequest(v); ok {
			p.Finals = finals(before, p.State)
			p.State = start(v, req, now)
			p.Started = true
			before = nil
		}
	}

	st := p.State
	if st != nil && st.FinishedAt == nil {
		p.step(v, st, now, admit, pace)
		if st.FinishedAt == nil && allFinal(st) {
			finish(&p, now)
		}
	}
	p.Finals = append(p.Finals, finals(before, p.State)...)
	return p
}

// step runs one pass of an unfinished state: incorporate, time out,
// republish, or dispatch the next season.
func (p *SeasonPlan) step(v SeasonView, st *catalogv1alpha1.SeasonSearchState, now time.Time,
	admit func(durable string, seq int64) dispatch.Decision, pace func(season int32) time.Time,
) {
	d := st.Dispatch
	cur := searching(st)

	// Incorporate the record that answers the dispatch in flight.
	if d.InFlight() && cur != nil {
		if rec := v.Records[cur.Number]; rec != nil && rec.Seq == d.Seq {
			at := metav1.NewTime(rec.FinishedAt.UTC().Truncate(time.Second))
			if at.IsZero() {
				at = metav1.NewTime(now.Truncate(time.Second))
			}
			d.AnsweredSeq, d.Withdrawn, d.Delivery = d.Seq, false, nil
			p.Answered, p.AnsweredSeq = true, d.Seq
			switch {
			case rec.State == recordstate.StateFailed:
				cur.Phase, cur.Reason = catalogv1alpha1.SeasonSearchFailed, catalogv1alpha1.SeasonSearchAgentFailed
			case rec.AllPaced && cur.Attempts < maxSeasonAttempts:
				// Every indexer was paced: no query was sent. The season goes
				// once more to the back of the queue (nextQueued).
				cur.Phase, cur.Reason = catalogv1alpha1.SeasonSearchQueued, ""
			default:
				cur.Phase, cur.AnsweredAt = catalogv1alpha1.SeasonSearchAnswered, &at
				cur.Candidates, cur.Approved = counts(rec)
				if rec.AllPaced {
					// Counts for no episode (§7).
					cur.Reason = catalogv1alpha1.SeasonSearchAllPaced
				}
			}
			cur = nil
		}
	}

	// A dispatch nobody answered closes as timed out.
	if d.InFlight() && now.Sub(d.DispatchedAt.Time) >= Timeout {
		d.AnsweredSeq, d.Withdrawn, d.Delivery = d.Seq, true, nil
		p.Answered, p.AnsweredSeq = true, d.Seq
		if cur != nil {
			cur.Phase, cur.Reason = catalogv1alpha1.SeasonSearchFailed, catalogv1alpha1.SeasonSearchTimedOut
			cur = nil
		}
	}

	// In flight: republish an unanswered task now and then.
	if d.InFlight() && cur != nil {
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
				t = seasonTask(v, cur.Number, d.Seq, now)
			}
			t.Republish, t.At = true, now
			p.Publish = t
			last = now
		}
		if next := last.Add(RepublishEvery); next.Before(p.Due) {
			p.Due = next
		}
		dispatch.Render(d, SeasonDurable, nil, nil, v.Key)
		return
	}
	if d != nil {
		d.Delivery = nil
	}
	if cur != nil {
		// A season reading Searching with no dispatch in flight (a state
		// written by hand, or a seq the ledger never saw) is searched again
		// rather than left to stall the request.
		cur.Phase = catalogv1alpha1.SeasonSearchQueued
	}

	// The next season: one at a time, behind its pace slot and the ledger.
	next := nextQueued(st)
	if next == nil {
		return
	}
	var recSeq, dSeq int64
	if rec := v.Records[next.Number]; rec != nil {
		recSeq = rec.Seq
	}
	if d != nil {
		dSeq = d.Seq
	}
	seq := max(recSeq+1, dSeq+1, now.UnixMilli())
	if slot := pace(next.Number); slot.After(now.Add(time.Second)) {
		p.Waiting, p.Due = catalogv1alpha1.DeliveryReasonPaced, slot
		dispatch.Render(d, "", &dispatch.Decision{
			Reason:  catalogv1alpha1.DeliveryReasonPaced,
			Message: "waiting for the namespace's live-search pace",
		}, nil, v.Key)
		return
	}
	dec := admit(SeasonDurable, seq)
	if !dec.Admitted {
		p.Waiting = dec.Reason
		p.Due = now.Add(max(dec.RetryAfter, time.Second))
		dispatch.Render(d, "", &dec, nil, v.Key)
		return
	}
	prev := int64(0)
	if d != nil {
		prev = d.AnsweredSeq
	}
	nd := &catalogv1alpha1.Dispatch{Seq: seq, AnsweredSeq: prev, DispatchedAt: metav1.NewTime(now.Truncate(time.Second))}
	dispatch.Render(nd, SeasonDurable, &dec, nil, v.Key)
	st.Dispatch = nd
	next.Phase, next.Reason, next.Seq = catalogv1alpha1.SeasonSearchSearching, "", seq
	next.Attempts = min(next.Attempts+1, maxSeasonAttempts)
	p.Publish = seasonTask(v, next.Number, seq, now)
	p.Due = now.Add(RepublishEvery)
}

// nextRequest is the oldest open request not deleting and not the one the
// state already served.
func nextRequest(v SeasonView) (SeasonRequest, bool) {
	for _, r := range v.Requests {
		if r.Deleting {
			continue
		}
		if v.State != nil && v.State.Request.UID == r.Search.UID {
			continue
		}
		return r, true
	}
	return SeasonRequest{}, false
}

// start is a new state for req: its seasons (the named one, or every
// season but the specials), each Queued or skipped at once with the reason
// a query could grab nothing (§4.3).
func start(v SeasonView, req SeasonRequest, now time.Time) *catalogv1alpha1.SeasonSearchState {
	st := &catalogv1alpha1.SeasonSearchState{Request: req.Search, StartedAt: metav1.NewTime(now.Truncate(time.Second))}
	facts := make(map[int32]SeasonFacts, len(v.Seasons))
	for _, f := range v.Seasons {
		facts[f.Number] = f
	}
	var numbers []int32
	if req.Season != nil {
		numbers = []int32{*req.Season}
	} else {
		for _, f := range v.Seasons {
			if f.Number != 0 {
				numbers = append(numbers, f.Number)
			}
		}
	}
	slices.Sort(numbers)
	numbers = slices.Compact(numbers)
	for _, n := range numbers {
		s := catalogv1alpha1.SeasonSearchSeason{Number: n, Phase: catalogv1alpha1.SeasonSearchQueued}
		f, ok := facts[n]
		switch {
		case !ok:
			s.Phase, s.Reason = catalogv1alpha1.SeasonSearchSkipped, catalogv1alpha1.SeasonSearchNoSuchSeason
		case !v.Monitored || !f.Monitored:
			s.Phase, s.Reason = catalogv1alpha1.SeasonSearchSkipped, catalogv1alpha1.SeasonSearchNotMonitored
		case f.Wanted == 0:
			s.Phase, s.Reason = catalogv1alpha1.SeasonSearchSkipped, catalogv1alpha1.SeasonSearchNothingWanted
		case f.Downloading >= f.Wanted:
			s.Phase, s.Reason = catalogv1alpha1.SeasonSearchSkipped, catalogv1alpha1.SeasonSearchAllDownloading
		}
		st.Seasons = append(st.Seasons, s)
	}
	if len(st.Seasons) > maxSeasons {
		st.Seasons = st.Seasons[:maxSeasons]
	}
	return st
}

// maxSeasons is SeasonSearchState.Seasons' MaxItems.
const maxSeasons = 200

// searching is the state's season in flight, nil for none.
func searching(st *catalogv1alpha1.SeasonSearchState) *catalogv1alpha1.SeasonSearchSeason {
	for i := range st.Seasons {
		if s := &st.Seasons[i]; s.Phase == catalogv1alpha1.SeasonSearchSearching {
			return s
		}
	}
	return nil
}

// nextQueued is the season due next: the lowest Queued season not yet
// dispatched, then the lowest sent back after every indexer paced it.
func nextQueued(st *catalogv1alpha1.SeasonSearchState) *catalogv1alpha1.SeasonSearchSeason {
	var best *catalogv1alpha1.SeasonSearchSeason
	for i := range st.Seasons {
		s := &st.Seasons[i]
		if s.Phase != catalogv1alpha1.SeasonSearchQueued {
			continue
		}
		if best == nil || s.Attempts < best.Attempts || (s.Attempts == best.Attempts && s.Number < best.Number) {
			best = s
		}
	}
	return best
}

func allFinal(st *catalogv1alpha1.SeasonSearchState) bool {
	for i := range st.Seasons {
		if !st.Seasons[i].Phase.Final() {
			return false
		}
	}
	return true
}

// finish closes the plan's state: finishedAt now, and lastSearchedAt the
// newest season answer (the stored one when none answered).
func finish(p *SeasonPlan, now time.Time) {
	st := p.State
	at := metav1.NewTime(now.Truncate(time.Second))
	st.FinishedAt = &at
	for i := range st.Seasons {
		s := &st.Seasons[i]
		if s.Phase != catalogv1alpha1.SeasonSearchAnswered || s.AnsweredAt == nil {
			continue
		}
		if p.LastSearchedAt == nil || s.AnsweredAt.After(p.LastSearchedAt.Time) {
			t := *s.AnsweredAt
			p.LastSearchedAt = &t
		}
	}
}

// counts is a season answer's candidates and approved releases.
func counts(rec *schema.SearchRecord) (candidates, approved int32) {
	for i := range rec.Candidates {
		if rec.Candidates[i].Decision.Approved {
			approved++
		}
	}
	return int32(len(rec.Candidates)), approved //nolint:gosec // capped at MaxSearchCandidates
}

// phasesOf is st's season phases by number, nil for no state.
func phasesOf(st *catalogv1alpha1.SeasonSearchState) map[int32]catalogv1alpha1.SeasonSearchPhase {
	if st == nil {
		return nil
	}
	out := make(map[int32]catalogv1alpha1.SeasonSearchPhase, len(st.Seasons))
	for _, s := range st.Seasons {
		out[s.Number] = s.Phase
	}
	return out
}

// finals are the seasons of st final now and not final before.
func finals(before map[int32]catalogv1alpha1.SeasonSearchPhase, st *catalogv1alpha1.SeasonSearchState) []SeasonFinal {
	if st == nil {
		return nil
	}
	var out []SeasonFinal
	for _, s := range st.Seasons {
		if !s.Phase.Final() {
			continue
		}
		if was, ok := before[s.Number]; ok && was.Final() {
			continue
		}
		out = append(out, SeasonFinal{Request: st.Request, Season: s})
	}
	return out
}

// seasonTask is the SearchTask v2 of season at seq (§5.1): the Series as
// the item, the season, a user-invoked live search on the high lane, Msg-Id
// events.MsgIDForSeasonSearch. Never IndexOnly, never a donor search.
func seasonTask(v SeasonView, season int32, seq int64, now time.Time) *Task {
	n := season
	payload := schema.SearchTask{
		MediaRef:    commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: v.Series.Name},
		Reason:      schema.SearchReasonSeason,
		UserInvoked: true,
		Item:        v.Series, Seq: seq, Season: &n,
		QualityProfile: v.Profile, Protocols: v.Protocols, IndexerPriorities: v.Priorities, Scope: v.Scope,
	}
	mediaKey := events.MediaKey(string(commonv1.MediaKindSeries), v.Series.Namespace, v.Series.Name)
	return &Task{
		Subject: events.WorkSearchSubject(events.PriorityHigh, mediaKey),
		MsgID:   events.MsgIDForSeasonSearch(v.Series.UID, season, seq),
		Payload: payload, Live: true, At: now,
	}
}

// SeasonPaceKey is a season task's key in the namespace's live-search pace
// class.
func SeasonPaceKey(seriesUID string, season int32) string {
	return seriesUID + "/" + schema.SeasonSub(season)
}
