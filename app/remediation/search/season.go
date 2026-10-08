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

package search

import (
	"context"
	"fmt"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/catalog/controller/wantedcron"
	"github.com/mediactl/clustarr/app/catalog/history"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	"github.com/mediactl/clustarr/app/catalog/seriessearch"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/dlindex"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/version"
)

// The season planner's Event reasons (series search design §11).
const (
	// ReasonSeasonSearchFailed is a Warning on the Series: a season of a
	// person's series search failed (the agent could not run it, or no
	// answer came).
	ReasonSeasonSearchFailed = "SeasonSearchFailed"
)

// The season search metric's trigger labels.
const (
	triggerSeries = "series"
	triggerSeason = "season"
)

// servedSearch is what the stage read of the planned Searches naming a
// Series: the open requests, oldest first, and each one's trigger.
type servedSearch struct {
	triggers map[string]string
}

// planSeries is the search stage's Series mode (series search design §4):
// the Series key's season planner over status.seasonSearch, which serves a
// person's planned Search one season task at a time. A Series has no
// automatic search of its own; this plans only person-requested searches,
// and publishes no Episode search.
func (s *Stage) planSeries(ctx context.Context, env *remediation.Env, v *remediation.ItemView) (remediation.ItemResult, error) {
	ser, ok := v.Item.(*catalogv1alpha1.Series)
	if !ok {
		return remediation.ItemResult{}, fmt.Errorf("search: a Series key holds %T", v.Item)
	}
	if ser.DeletionTimestamp != nil {
		// A deleting Series dispatches nothing; its search state is
		// re-sent, so the set stays complete.
		return remediation.ItemResult{Sets: []itempass.Set{seasonSet(ser.Status.SeasonSearch, ser.Status.LastSearchedAt)}}, nil
	}
	now := v.Now.Time
	sv, served, err := s.gatherSeries(ctx, ser, v, now)
	if err != nil {
		return remediation.ItemResult{}, remediation.Transient(err)
	}
	admit := func(durable string, seq int64) dispatch.Decision {
		if s.o.Dispatch == nil {
			return dispatch.Decision{Admitted: true}
		}
		return s.o.Dispatch.Admit(durable, sv.Key, seq)
	}
	class := records.SearchClass(ser.Namespace)
	pace := func(season int32) time.Time {
		if env == nil || env.Pacer == nil {
			return now
		}
		return env.Pacer.Reserve(class, searchplan.SeasonPaceKey(sv.Series.UID, season))
	}
	plan := searchplan.DecideSeasons(sv, admit, pace)
	return s.seriesResult(sv, plan, class, served), nil
}

// seasonSet is catalog-grab's search half on a Series: seasonSearch and
// lastSearchedAt, both always named (nil when absent), beside the grab
// stage's pendingGrabs -- one complete declaration.
func seasonSet(st *catalogv1alpha1.SeasonSearchState, last *metav1.Time) itempass.Set {
	return itempass.Set{Manager: k8s.ManagerCatalogGrab, Fields: map[string]any{
		"seasonSearch":   nilIf(st == nil, st),
		"lastSearchedAt": nilIf(last == nil, last),
	}}
}

// seriesResult is the season plan as the pass's outcome.
func (s *Stage) seriesResult(sv searchplan.SeasonView, plan searchplan.SeasonPlan, class string, served servedSearch) remediation.ItemResult {
	st := plan.State
	if st != nil && st.Dispatch.InFlight() && s.o.Book != nil {
		// A nak or a dead letter the advisory intake recorded.
		dispatch.Render(st.Dispatch, "", nil, s.o.Book, sv.Key)
	}
	res := remediation.ItemResult{Sets: []itempass.Set{seasonSet(st, plan.LastSearchedAt)}, Due: plan.Due}
	if plan.Waiting != "" && s.o.Dispatch != nil {
		s.o.Dispatch.Waiting(searchplan.SeasonDurable, sv.Key, plan.Waiting)
	}
	if plan.Answered {
		res.Effects = append(res.Effects, remediation.DispatchAnswered{Durable: searchplan.SeasonDurable, Ledger: sv.Key, Seq: plan.AnsweredSeq})
	}
	if t := plan.Publish; t != nil && t.Payload.Season != nil {
		if eff, ok := publishTask(t, sv.Key, sv.Series, searchplan.SeasonDurable, class, searchplan.SeasonPaceKey(sv.Series.UID, *t.Payload.Season)); ok {
			res.Effects = append(res.Effects, eff)
			s.published.set(sv.Key, t)
		}
	}
	for _, f := range plan.Finals {
		trigger := served.triggers[f.Request.UID]
		if trigger == "" {
			trigger = inferTrigger(st, f.Request)
		}
		metrics.SeasonSearchesTotal.WithLabelValues(trigger, seasonOutcome(f.Season)).Inc()
		if f.Season.Phase == catalogv1alpha1.SeasonSearchFailed {
			res.Events = append(res.Events, remediation.ItemEvent{
				Recorder: "series", Type: "Warning", Reason: ReasonSeasonSearchFailed, Action: "Search",
				Message: fmt.Sprintf("season %d of the search %s failed: %s", f.Season.Number, f.Request.Name, f.Season.Reason),
			})
		}
	}
	return res
}

// inferTrigger is a final's trigger when its Search is gone: "season" for
// a state of one season, else "series".
func inferTrigger(st *catalogv1alpha1.SeasonSearchState, req catalogv1alpha1.SeasonSearchRequest) string {
	if st != nil && st.Request.UID == req.UID && len(st.Seasons) == 1 {
		return triggerSeason
	}
	return triggerSeries
}

// seasonOutcome is a final season's metric outcome.
func seasonOutcome(s catalogv1alpha1.SeasonSearchSeason) string {
	switch s.Phase {
	case catalogv1alpha1.SeasonSearchAnswered:
		if s.Reason == catalogv1alpha1.SeasonSearchAllPaced {
			return "paced"
		}
		return "answered"
	case catalogv1alpha1.SeasonSearchSkipped:
		return "skipped"
	case catalogv1alpha1.SeasonSearchFailed:
		if s.Reason == catalogv1alpha1.SeasonSearchTimedOut {
			return "timedOut"
		}
		return "failed"
	default:
		return "canceled"
	}
}

// gatherSeries builds searchplan's season view of one Series from the
// cache, its season records and the leader-local publish book.
func (s *Stage) gatherSeries(ctx context.Context, ser *catalogv1alpha1.Series, v *remediation.ItemView, now time.Time) (searchplan.SeasonView, servedSearch, error) {
	r := s.o.Reader
	ref := itemread.Ref(ser)
	sv := searchplan.SeasonView{
		Series: ref, Now: now,
		Monitored:    ptr.Deref(ser.Spec.Monitored, true),
		Anime:        ser.Spec.SeriesType == catalogv1alpha1.SeriesTypeAnime,
		Scope:        schema.BlockScopeOf(ref),
		State:        ser.Status.SeasonSearch,
		LastSearched: ser.Status.LastSearchedAt,
		Key:          dispatch.Key{Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name, Sub: history.DispatchSubSeasonSearch},
	}
	served := servedSearch{triggers: map[string]string{}}

	// The planned Searches naming the series.
	searches, err := seriessearch.Of(ctx, r, ser.Namespace, ser.Name)
	if err != nil {
		return sv, served, err
	}
	servedFound := false
	for i := range searches {
		srch := &searches[i]
		uid := string(srch.UID)
		trigger := triggerSeries
		if srch.Spec.Season != nil {
			trigger = triggerSeason
		}
		served.triggers[uid] = trigger
		deleting := srch.DeletionTimestamp != nil
		settled := srch.Status.Phase == catalogv1alpha1.SearchPhaseCompleted || srch.Status.Phase == catalogv1alpha1.SearchPhaseFailed
		if st := sv.State; st != nil && st.Request.UID == uid {
			servedFound = true
			sv.ServedGone = deleting
			sv.ServedSettled = settled || deleting
		}
		if settled {
			continue
		}
		req := searchplan.SeasonRequest{
			Search:   catalogv1alpha1.SeasonSearchRequest{Name: srch.Name, UID: uid},
			Created:  srch.CreationTimestamp.UTC(),
			Deleting: deleting,
		}
		if srch.Spec.Season != nil {
			n := *srch.Spec.Season
			req.Season = &n
		}
		sv.Requests = append(sv.Requests, req)
	}
	if sv.State != nil && !servedFound {
		sv.ServedGone, sv.ServedSettled = true, true
	}

	// The profile, protocols and priorities the season tasks carry.
	cfg, err := itemread.ConfigOf(ctx, r, ser)
	if err != nil {
		return sv, served, err
	}
	if _, err := itemread.Profile(ctx, r, cfg.QualityProfileRef); err == nil {
		sv.Profile = cfg.QualityProfileRef
	}
	protocols, err := itemread.Protocols(ctx, r, ser.Namespace)
	if err != nil {
		return sv, served, err
	}
	for p, on := range protocols {
		if on {
			sv.Protocols = append(sv.Protocols, p)
		}
	}
	slices.Sort(sv.Protocols)
	idxs, err := itemread.Indexers(ctx, r, ser.Namespace)
	if err != nil {
		return sv, served, err
	}
	sv.Priorities = itemread.Priorities(idxs)

	// Only a request about to start, or a state in progress, needs the
	// season facts and records.
	if st := sv.State; st == nil || st.FinishedAt != nil {
		if len(sv.Requests) == 0 {
			return sv, served, nil
		}
	}
	if err := s.seasonFacts(ctx, ser, v, now, &sv); err != nil {
		return sv, served, err
	}
	if st := sv.State; st != nil && st.FinishedAt == nil {
		sv.Records = map[int32]*schema.SearchRecord{}
		for _, se := range st.Seasons {
			if se.Phase != catalogv1alpha1.SeasonSearchSearching && se.Phase != catalogv1alpha1.SeasonSearchQueued {
				continue
			}
			rec, _, ok, err := s.searches.Get(ctx, agentrecords.SeasonSearchKey(ref.UID, se.Number))
			if err != nil {
				return sv, served, fmt.Errorf("search: season %d record of %s: %w", se.Number, ser.Name, err)
			}
			if ok {
				sv.Records[se.Number] = rec
			}
		}
		if d := st.Dispatch; d.InFlight() {
			sv.InFlight = s.published.get(sv.Key, d.Seq)
		}
	}
	return sv, served, nil
}

// seasonFacts fills sv.Seasons from the Series' seasons and Episodes: per
// season, monitored (the series and an episode of it), wanted (monitored,
// aired, missing or below its cutoff -- wantedcron's rule -- or covered by
// a live grab) and downloading (of the wanted, covered by a live video
// entry of this pass's downloads outcome).
func (s *Stage) seasonFacts(ctx context.Context, ser *catalogv1alpha1.Series, v *remediation.ItemView, now time.Time, sv *searchplan.SeasonView) error {
	var eps catalogv1alpha1.EpisodeList
	if err := s.o.Reader.List(ctx, &eps, client.InNamespace(ser.Namespace), client.MatchingFields{series.EpisodeBySeriesRefIndex: ser.Name}); err != nil {
		return fmt.Errorf("search: list the Episodes of %s: %w", ser.Name, err)
	}
	entries := dlindex.Entries(ser)
	if v.Downloads != nil {
		entries = v.Downloads.Entries
	}
	facts := map[int32]*searchplan.SeasonFacts{}
	fact := func(n int32) *searchplan.SeasonFacts {
		f, ok := facts[n]
		if !ok {
			f = &searchplan.SeasonFacts{Number: n}
			facts[n] = f
		}
		return f
	}
	for _, se := range ser.Status.Seasons {
		fact(se.Number)
	}
	for i := range eps.Items {
		ep := &eps.Items[i]
		f := fact(ep.Spec.SeasonNumber)
		monitored := sv.Monitored && itemread.OwnMonitored(ep)
		f.Monitored = f.Monitored || monitored
		if !monitored || !itemread.Available(ep, now) {
			continue
		}
		live := false
		for j := range entries {
			e := &entries[j]
			if rollup.EntryNonTerminal(e) && !rollup.IsDonor(e) && itemread.Covers(e, ep) {
				live = true
				break
			}
		}
		cand, _ := wantedcron.CandidateOf(ep, now)
		if cand.Reason == "" && !live {
			continue
		}
		f.Wanted++
		if live {
			f.Downloading++
		}
	}
	sv.Seasons = sv.Seasons[:0]
	for _, f := range facts {
		sv.Seasons = append(sv.Seasons, *f)
	}
	slices.SortFunc(sv.Seasons, func(a, b searchplan.SeasonFacts) int { return int(a.Number) - int(b.Number) })
	return nil
}

// seasonOf is the Episode's season inputs (series search design §7): its
// season pending in the Series' season search (Queued or Searching), and
// when the season was answered (not AllPaced or NoIndexer: no indexer was
// asked, so it is no episode's search).
func seasonOf(owner client.Object, ep *catalogv1alpha1.Episode) (pending bool, answeredAt *metav1.Time) {
	ser, ok := owner.(*catalogv1alpha1.Series)
	if !ok || ser.Status.SeasonSearch == nil {
		return false, nil
	}
	for _, se := range ser.Status.SeasonSearch.Seasons {
		if se.Number != ep.Spec.SeasonNumber {
			continue
		}
		switch se.Phase {
		case catalogv1alpha1.SeasonSearchQueued, catalogv1alpha1.SeasonSearchSearching:
			return true, nil
		case catalogv1alpha1.SeasonSearchAnswered:
			if se.Reason != catalogv1alpha1.SeasonSearchAllPaced && se.Reason != catalogv1alpha1.SeasonSearchNoIndexer &&
				se.AnsweredAt != nil {
				at := *se.AnsweredAt
				return false, &at
			}
		}
	}
	return false, nil
}

// publishTask is t's DispatchPublish on durable: the ledger confirms its
// slot when it lands, and a live search consumes its pacing reservation
// (paceClass, paceKey).
func publishTask(t *searchplan.Task, key dispatch.Key, item schema.ItemRef, durable, paceClass, paceKey string) (remediation.Effect, bool) {
	name, data, err := schema.Encode(t.Payload)
	if err != nil {
		return nil, false
	}
	eff := remediation.DispatchPublish{
		Publish: remediation.Publish{
			Subject: t.Subject, MsgID: t.MsgID,
			Envelope: &events.Envelope{
				ID: t.MsgID, Type: "catalog.SearchTask", Schema: name, Source: "manager@" + version.String(),
				Key: item.Namespace + "/" + item.Name, Time: t.At.UTC(), Data: data,
			},
		},
		Durable: durable, Ledger: key, Seq: t.Payload.Seq,
	}
	if t.Live {
		eff.Pace, eff.PaceKey = paceClass, paceKey
	}
	return eff, true
}

// SeasonLedgerSource rebuilds catalog-search-high's outstanding season
// dispatches from every Series' status.seasonSearch.dispatch (seq >
// answeredSeq) in the cache, after a leader change (series search design
// §4.5). The Search controller's interactive tasks on the same durable
// stay unledgered.
type SeasonLedgerSource struct{}

var _ dispatch.Source = SeasonLedgerSource{}

// Durable implements dispatch.Source.
func (SeasonLedgerSource) Durable() string { return searchplan.SeasonDurable }

// Rebuild implements dispatch.Source.
func (SeasonLedgerSource) Rebuild(ctx context.Context, r client.Reader) ([]dispatch.Outstanding, error) {
	var list catalogv1alpha1.SeriesList
	if err := r.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("search: rebuild the season ledger: %w", err)
	}
	var out []dispatch.Outstanding
	for i := range list.Items {
		ser := &list.Items[i]
		st := ser.Status.SeasonSearch
		if st == nil || !st.Dispatch.InFlight() {
			continue
		}
		out = append(out, dispatch.Outstanding{
			Key: dispatch.Key{Kind: "Series", Namespace: ser.Namespace, Name: ser.Name, Sub: history.DispatchSubSeasonSearch},
			Seq: st.Dispatch.Seq,
		})
	}
	return out, nil
}
