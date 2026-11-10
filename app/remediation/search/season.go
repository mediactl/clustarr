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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
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
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/quality"
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
	triggerSeries    = "series"
	triggerSeason    = "season"
	triggerAutomatic = "automatic"
)

// servedSearch is what the stage read of the planned Searches naming a
// Series: the open requests, oldest first, and each one's trigger.
type servedSearch struct {
	triggers map[string]string
}

// planContainer is the search stage's container mode (series search design
// §4): the Series key's season planner over status.seasonSearch, which
// serves a person's planned Search one season task at a time and, with none
// waiting, the automatic season search of its PackFirst seasons (owner,
// 2026-10-27; §17); and since 2026-11-10 (design D10) the Comic key's
// volume planner over status.volumeSearch, the same plan over the one
// season that is the volume, automatic only. It publishes no Episode or
// Issue search.
func (s *Stage) planContainer(ctx context.Context, env *remediation.Env, v *remediation.ItemView) (remediation.ItemResult, error) {
	switch v.Item.(type) {
	case *catalogv1alpha1.Series, *catalogv1alpha1.Comic:
	default:
		return remediation.ItemResult{}, fmt.Errorf("search: a %s key holds %T", v.Key.Kind, v.Item)
	}
	if v.Item.GetDeletionTimestamp() != nil {
		// A deleting container dispatches nothing; its search state is
		// re-sent, so the set stays complete.
		return remediation.ItemResult{Sets: []itempass.Set{containerSet(v.Key.Kind, itemread.ContainerSearch(v.Item), lastSearchedOf(v.Item))}}, nil
	}
	now := v.Now.Time
	sv, served, err := s.gatherContainer(ctx, v, now)
	if err != nil {
		return remediation.ItemResult{}, remediation.Transient(err)
	}
	admit := func(durable string, seq int64) dispatch.Decision {
		if s.o.Dispatch == nil {
			return dispatch.Decision{Admitted: true}
		}
		return s.o.Dispatch.Admit(durable, sv.Key, seq)
	}
	class := records.SearchClass(v.Item.GetNamespace())
	pace := func(season int32) time.Time {
		if env == nil || env.Pacer == nil {
			return now
		}
		return env.Pacer.Reserve(class, searchplan.SeasonPaceKey(sv.Series.UID, season))
	}
	plan := searchplan.DecideSeasons(sv, admit, pace)
	return s.containerResult(v.Key.Kind, sv, plan, class, served), nil
}

// containerSet is catalog-grab's search half on a container, beside the
// grab stage's pendingGrabs -- one complete declaration: on a Series,
// seasonSearch and lastSearchedAt, both always named (nil when absent); on
// a Comic, volumeSearch (a Comic carries no lastSearchedAt).
func containerSet(kind remediation.KeyKind, st *catalogv1alpha1.SeasonSearchState, last *metav1.Time) itempass.Set {
	if kind == remediation.KindComic {
		return itempass.Set{Manager: k8s.ManagerCatalogGrab, Fields: map[string]any{
			"volumeSearch": nilIf(st == nil, st),
		}}
	}
	return itempass.Set{Manager: k8s.ManagerCatalogGrab, Fields: map[string]any{
		"seasonSearch":   nilIf(st == nil, st),
		"lastSearchedAt": nilIf(last == nil, last),
	}}
}

// lastSearchedOf is a container's status.lastSearchedAt: a Series' own, nil
// for a Comic, which has none.
func lastSearchedOf(o client.Object) *metav1.Time {
	if ser, ok := o.(*catalogv1alpha1.Series); ok {
		return ser.Status.LastSearchedAt
	}
	return nil
}

// containerResult is the season (or volume) plan as the pass's outcome.
func (s *Stage) containerResult(kind remediation.KeyKind, sv searchplan.SeasonView, plan searchplan.SeasonPlan, class string, served servedSearch) remediation.ItemResult {
	st := plan.State
	if st != nil && st.Dispatch.InFlight() && s.o.Book != nil {
		// A nak or a dead letter the advisory intake recorded.
		dispatch.Render(st.Dispatch, "", nil, s.o.Book, sv.Key)
	}
	res := remediation.ItemResult{Sets: []itempass.Set{containerSet(kind, st, plan.LastSearchedAt)}, Due: plan.Due}
	if plan.Waiting != "" && s.o.Dispatch != nil {
		s.o.Dispatch.Waiting(searchplan.SeasonDurable, sv.Key, plan.Waiting)
	}
	if plan.Answered {
		res.Effects = append(res.Effects, remediation.DispatchAnswered{Durable: searchplan.SeasonDurable, Ledger: sv.Key, Seq: plan.AnsweredSeq})
	} else if st != nil {
		if a, ok := s.unanswered(searchplan.SeasonDurable, sv.Key, st.Dispatch); ok {
			res.Effects = append(res.Effects, a)
		}
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
		if f.Season.Phase == catalogv1alpha1.SeasonSearchAnswered && f.Season.Approved > 0 {
			// The grab stage ran before this one and reads a season answer
			// only from the stored state, and nothing wakes a Series for
			// its own seasonSearch: the next pass comes at once, inside
			// grabplan.CandidateWindow, or the answer ages out ungrabbed
			// (2026-11-02: 91 Days' 101 approved releases, and 61 other
			// series' automatic answers of 2026-10-28).
			res.Again = true
		}
		if f.Season.Phase == catalogv1alpha1.SeasonSearchFailed {
			msg := fmt.Sprintf("season %d of the search %s failed: %s", f.Season.Number, f.Request.Name, f.Season.Reason)
			recorder := "series"
			if kind == remediation.KindComic {
				msg, recorder = fmt.Sprintf("the volume search failed: %s", f.Season.Reason), "comic"
			}
			res.Events = append(res.Events, remediation.ItemEvent{
				Recorder: recorder, Type: "Warning", Reason: ReasonSeasonSearchFailed, Action: "Search", Message: msg,
			})
		}
	}
	return res
}

// inferTrigger is a final's trigger when its Search is gone: "automatic"
// for the automatic season search, "season" for a state of one season, else
// "series".
func inferTrigger(st *catalogv1alpha1.SeasonSearchState, req catalogv1alpha1.SeasonSearchRequest) string {
	if req.UID == "" {
		return triggerAutomatic
	}
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

// gatherContainer builds searchplan's container view of one Series or
// Comic from the cache, its season (volume) records and the leader-local
// publish book.
func (s *Stage) gatherContainer(ctx context.Context, v *remediation.ItemView, now time.Time) (searchplan.SeasonView, servedSearch, error) {
	r := s.o.Reader
	item := v.Item
	ref := itemread.Ref(item)
	sv := searchplan.SeasonView{
		Series: ref, Kind: itemread.MediaKind(item), Now: now,
		Monitored:    itemread.OwnMonitored(item),
		Scope:        schema.BlockScopeOf(ref),
		State:        itemread.ContainerSearch(item),
		LastSearched: lastSearchedOf(item),
		Key:          dispatch.Key{Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name, Sub: history.DispatchSubSeasonSearch},
	}
	served := servedSearch{triggers: map[string]string{}}
	switch t := item.(type) {
	case *catalogv1alpha1.Series:
		sv.Anime = t.Spec.SeriesType == catalogv1alpha1.SeriesTypeAnime
		if err := s.plannedRequests(ctx, t, &sv, served); err != nil {
			return sv, served, err
		}
	case *catalogv1alpha1.Comic:
		// A Comic has no planned request (its Search CR fans out per Issue,
		// SearchSpec.ContainerKind): its state is the automatic volume
		// search's, with nothing to cancel it and no handshake to wait for.
		if sv.State != nil {
			sv.ServedGone, sv.ServedSettled = !searchplan.Automatic(sv.State), true
		}
	}

	// The profile, protocols and priorities the season tasks carry.
	cfg, err := itemread.ConfigOf(ctx, r, item)
	if err != nil {
		return sv, served, err
	}
	prof, err := itemread.ItemProfile(ctx, r, item, cfg.QualityProfileRef)
	if err == nil {
		sv.Profile = cfg.QualityProfileRef
	}
	protocols, err := itemread.Protocols(ctx, r, ref.Namespace)
	if err != nil {
		return sv, served, err
	}
	for p, on := range protocols {
		if on {
			sv.Protocols = append(sv.Protocols, p)
		}
	}
	slices.Sort(sv.Protocols)
	idxs, err := itemread.Indexers(ctx, r, ref.Namespace)
	if err != nil {
		return sv, served, err
	}
	sv.Priorities = itemread.Priorities(idxs)

	// Only a request about to start, an automatic state that could (a
	// monitored container with a PackFirst season, or volume), or a state in
	// progress needs the season facts and records.
	if st := sv.State; st == nil || st.FinishedAt != nil {
		if len(sv.Requests) == 0 && (!sv.Monitored || !anyPackFirst(item)) {
			return sv, served, nil
		}
	}
	switch t := item.(type) {
	case *catalogv1alpha1.Series:
		if err := s.seasonFacts(ctx, t, v, now, &sv, prof); err != nil {
			return sv, served, err
		}
	case *catalogv1alpha1.Comic:
		if err := s.volumeFacts(ctx, t, v, now, &sv, prof); err != nil {
			return sv, served, err
		}
	}
	if st := sv.State; st != nil && st.FinishedAt == nil {
		sv.Records = map[int32]*schema.SearchRecord{}
		for _, se := range st.Seasons {
			if se.Phase != catalogv1alpha1.SeasonSearchSearching && se.Phase != catalogv1alpha1.SeasonSearchQueued {
				continue
			}
			rec, _, ok, err := s.searches.Get(ctx, agentrecords.SeasonSearchKey(ref.UID, se.Number))
			if err != nil {
				return sv, served, fmt.Errorf("search: season %d record of %s: %w", se.Number, ref.Name, err)
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

// plannedRequests fills sv.Requests with the planned Searches naming the
// series (seriessearch's index), oldest first, each one's trigger in
// served, and the handshake state of the request sv.State serves.
func (s *Stage) plannedRequests(ctx context.Context, ser *catalogv1alpha1.Series, sv *searchplan.SeasonView, served servedSearch) error {
	searches, err := seriessearch.Of(ctx, s.o.Reader, ser.Namespace, ser.Name)
	if err != nil {
		return err
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
		// An automatic state names no Search: nothing to cancel it, and no
		// handshake to wait for once it finishes.
		sv.ServedGone, sv.ServedSettled = !searchplan.Automatic(sv.State), true
	}
	return nil
}

// seasonFacts fills sv.Seasons from the Series' seasons and Episodes: per
// season, monitored (the series and an episode of it), wanted (monitored,
// aired, missing or below its cutoff -- wantedcron's rule -- or covered by
// a live grab) and downloading (of the wanted, covered by a live video
// entry of this pass's downloads outcome that holds the season against a
// pack, holdsSeason).
func (s *Stage) seasonFacts(ctx context.Context, ser *catalogv1alpha1.Series, v *remediation.ItemView, now time.Time, sv *searchplan.SeasonView, prof *quality.Profile) error {
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
		fact(se.Number).PackFirst = searchplan.PackFirst(se)
	}
	for i := range eps.Items {
		ep := &eps.Items[i]
		f := fact(ep.Spec.SeasonNumber)
		monitored := sv.Monitored && itemread.OwnMonitored(ep)
		f.Monitored = f.Monitored || monitored
		if !monitored || !itemread.Available(ep, now) {
			continue
		}
		live, holding := false, false
		for j := range entries {
			e := &entries[j]
			if rollup.EntryNonTerminal(e) && !rollup.IsDonor(e) && itemread.Covers(e, ep) {
				live = true
				holding = holding || holdsSeason(prof, seriesOriginal(ser), e)
			}
		}
		cand, _ := wantedcron.CandidateOf(ep, now)
		if cand.Reason == "" && !live {
			continue
		}
		f.Wanted++
		if holding {
			f.Downloading++
		}
		if !live && replacesAsPack(ep, now) {
			f.ReplaceDue++
		}
		if _, granted := s.o.Backlog.Granted(string(ep.UID), now); !live && granted {
			f.BacklogDue++
		}
		// An episode added unsearched asks the release index alone and
		// waits for no pack query (searchplan.View.NoSearchOnAdd), so it
		// asks for none.
		if ep.Status.SearchAttempts.Count == 0 && ep.Status.SearchAttempts.Latest == nil && ep.Status.LastSearchedAt == nil &&
			!addedUnsearchedUnder(ep, ser) {
			f.FirstSearch++
		}
	}
	sv.Seasons = sv.Seasons[:0]
	for _, f := range facts {
		sv.Seasons = append(sv.Seasons, *f)
	}
	slices.SortFunc(sv.Seasons, func(a, b searchplan.SeasonFacts) int { return int(a.Number) - int(b.Number) })
	return nil
}

// volumeFacts fills sv.Seasons with the Comic's one season, its volume
// (searchplan.VolumeNumber; 2026-11-10 design D10), from its Issues as
// seasonFacts reads a Series' Episodes: monitored (the comic and an issue),
// wanted (monitored, released, missing or below its cutoff -- wantedcron's
// rule -- or covered by a live grab), downloading (of the wanted, covered
// by a live video entry that holds the volume, holdsSeason) and never
// searched (FirstSearch). PackFirst is the Comic's VolumePackFirst.
func (s *Stage) volumeFacts(ctx context.Context, co *catalogv1alpha1.Comic, v *remediation.ItemView, now time.Time, sv *searchplan.SeasonView, prof *quality.Profile) error {
	var issues catalogv1alpha1.IssueList
	if err := s.o.Reader.List(ctx, &issues, client.InNamespace(co.Namespace), client.MatchingFields{comic.IssueByComicRefIndex: co.Name}); err != nil {
		return fmt.Errorf("search: list the Issues of %s: %w", co.Name, err)
	}
	entries := dlindex.Entries(co)
	if v.Downloads != nil {
		entries = v.Downloads.Entries
	}
	f := searchplan.SeasonFacts{Number: searchplan.VolumeNumber, PackFirst: searchplan.VolumePackFirst(co)}
	for i := range issues.Items {
		iss := &issues.Items[i]
		monitored := sv.Monitored && itemread.OwnMonitored(iss)
		f.Monitored = f.Monitored || monitored
		if !monitored || !itemread.Available(iss, now) {
			continue
		}
		live, holding := false, false
		for j := range entries {
			e := &entries[j]
			if rollup.EntryNonTerminal(e) && !rollup.IsDonor(e) && itemread.Covers(e, iss) {
				live = true
				holding = holding || holdsSeason(prof, "", e)
			}
		}
		cand, _ := wantedcron.CandidateOf(iss, now)
		if cand.Reason == "" && !live {
			continue
		}
		f.Wanted++
		if holding {
			f.Downloading++
		}
		if iss.Status.SearchAttempts.Count == 0 && iss.Status.SearchAttempts.Latest == nil && iss.Status.LastSearchedAt == nil &&
			!addedUnsearchedUnder(iss, co) {
			f.FirstSearch++
		}
	}
	sv.Seasons = []searchplan.SeasonFacts{f}
	return nil
}

// volumeOf is the Issue's volume inputs (design D10, the Episode's seasonOf
// with the season layer removed): the volume pending in the Comic's volume
// search (Queued or Searching), when it was answered (not AllPaced or
// NoIndexer: no indexer was asked, so it is no issue's search), and whether
// the Comic passes VolumePackFirst with no grab holding the volume
// (volumeDownloading).
func volumeOf(owner client.Object, prof *quality.Profile) (pending bool, answeredAt *metav1.Time, packFirst bool) {
	co, ok := owner.(*catalogv1alpha1.Comic)
	if !ok {
		return false, nil, false
	}
	packFirst = searchplan.VolumePackFirst(co) && !volumeDownloading(co, prof)
	if co.Status.VolumeSearch == nil {
		return false, nil, packFirst
	}
	for _, se := range co.Status.VolumeSearch.Seasons {
		if se.Number != searchplan.VolumeNumber {
			continue
		}
		switch se.Phase {
		case catalogv1alpha1.SeasonSearchQueued, catalogv1alpha1.SeasonSearchSearching:
			return true, nil, packFirst
		case catalogv1alpha1.SeasonSearchAnswered:
			if se.Reason != catalogv1alpha1.SeasonSearchAllPaced && se.Reason != catalogv1alpha1.SeasonSearchNoIndexer &&
				se.AnsweredAt != nil {
				at := *se.AnsweredAt
				return false, &at, packFirst
			}
		}
	}
	return false, nil, packFirst
}

// volumeDownloading reports a live video grab on co covering an issue that
// holds the volume (holdsSeason), as seasonDownloading reads a season.
func volumeDownloading(co *catalogv1alpha1.Comic, prof *quality.Profile) bool {
	entries := dlindex.Entries(co)
	for i := range entries {
		e := &entries[i]
		if rollup.EntryNonTerminal(e) && !rollup.IsDonor(e) && len(e.Issues) > 0 && holdsSeason(prof, "", e) {
			return true
		}
	}
	return false
}

// seasonOf is the Episode's season inputs (series search design §7): its
// season pending in the Series' season search (Queued or Searching), when
// the season was answered (not AllPaced or NoIndexer: no indexer was asked,
// so it is no episode's search), and whether the season passes PackFirst in
// the Series' status.seasons, the test the automatic season search reads,
// with no grab of it downloading that holds it (seasonDownloading).
func seasonOf(owner client.Object, ep *catalogv1alpha1.Episode, prof *quality.Profile) (pending bool, answeredAt *metav1.Time, packFirst bool) {
	ser, ok := owner.(*catalogv1alpha1.Series)
	if !ok {
		return false, nil, false
	}
	for _, s := range ser.Status.Seasons {
		if s.Number == ep.Spec.SeasonNumber {
			packFirst = searchplan.PackFirst(s) && !seasonDownloading(ser, s.Number, prof)
		}
	}
	if ser.Status.SeasonSearch == nil {
		return false, nil, packFirst
	}
	for _, se := range ser.Status.SeasonSearch.Seasons {
		if se.Number != ep.Spec.SeasonNumber {
			continue
		}
		switch se.Phase {
		case catalogv1alpha1.SeasonSearchQueued, catalogv1alpha1.SeasonSearchSearching:
			return true, nil, packFirst
		case catalogv1alpha1.SeasonSearchAnswered:
			if se.Reason != catalogv1alpha1.SeasonSearchAllPaced && se.Reason != catalogv1alpha1.SeasonSearchNoIndexer &&
				se.AnsweredAt != nil {
				at := *se.AnsweredAt
				return false, &at, packFirst
			}
		}
	}
	return false, nil, packFirst
}

// seasonDownloading reports a live video grab on ser covering an episode
// of season that holds it (holdsSeason). The automatic season search asks
// for no pack of such a season, since the grab planner would refuse one
// without its languages beside the grab (decision.ReasonEpisodeDownloading),
// so its episodes wait for no season query and search alone.
func seasonDownloading(ser *catalogv1alpha1.Series, season int32, prof *quality.Profile) bool {
	entries := dlindex.Entries(ser)
	for i := range entries {
		e := &entries[i]
		if !rollup.EntryNonTerminal(e) || rollup.IsDonor(e) || !holdsSeason(prof, seriesOriginal(ser), e) {
			continue
		}
		for _, n := range e.Episodes {
			if n.Season == season {
				return true
			}
		}
	}
	return false
}

// holdsSeason reports a live video grab that holds its season against a
// pack: under a profile taking complete audio first, one whose release
// carries every wanted language, which a pack without them is refused
// beside (decision.AudioOutranked). Any other live single is no hold: a
// pack is grabbed over it and the single removed (owner, 2026-11-03, after
// 2026-11-02's Remake our Life!, whose EMBER pack waited on SENPAI's
// Japanese-only singles).
func holdsSeason(prof *quality.Profile, originalTag string, e *catalogv1alpha1.DownloadEntry) bool {
	return prof != nil && decision.AudioOutranked(context.Background(), *prof, originalTag, false, decision.Queued{Languages: e.Release.Languages})
}

// replacesAsPack reports an Episode holding a file a whole release replaces
// -- below the cutoff because no GPU here decodes its video or it lacks a
// wanted audio language (rollup.ReasonUndecodableVideo, ReasonMissingAudio)
// -- whose own search is due on the wanted backoff (owner, 2026-11-03: the
// undecodable files and the dubbed anime missing dual audio "should be
// under the cutoff - forced to search for a new release", and the automatic
// season search counts such episodes as wanted, so a season of them is
// searched as a pack). A season with two or more such episodes is asked
// for (automaticSeasons, SeasonFacts.ReplaceDue), and they wait for it
// (seasonReplaces).
func replacesAsPack(ep *catalogv1alpha1.Episode, now time.Time) bool {
	if ep.Status.CutoffMet || (ep.Status.CutoffReason != rollup.ReasonUndecodableVideo && ep.Status.CutoffReason != rollup.ReasonMissingAudio) {
		return false
	}
	cand, _ := wantedcron.CandidateOf(ep, now)
	return cand.Reason == schema.SearchReasonCutoffUnmet && wantedcron.Eligible(cand.Attempts, now)
}

// seasonReplaces reports an Episode that waits for its season's pack query
// rather than search alone (searchplan.View.SeasonReplaces): it and at
// least one more monitored, aired episode of its season with no live grab
// replace as a pack (replacesAsPack), and no grab holds the season
// (seasonDownloading) -- the test seasonFacts' ReplaceDue applies, so the
// Series asks for the season while they wait, and its answer is each one's
// search.
func seasonReplaces(ctx context.Context, r client.Reader, ser *catalogv1alpha1.Series, ep *catalogv1alpha1.Episode, prof *quality.Profile, now time.Time) (bool, error) {
	if !ptr.Deref(ser.Spec.Monitored, true) || !replacesAsPack(ep, now) || seasonDownloading(ser, ep.Spec.SeasonNumber, prof) {
		return false, nil
	}
	var eps catalogv1alpha1.EpisodeList
	if err := r.List(ctx, &eps, client.InNamespace(ser.Namespace), client.MatchingFields{series.EpisodeBySeriesRefIndex: ser.Name}); err != nil {
		return false, fmt.Errorf("search: list the Episodes of %s: %w", ser.Name, err)
	}
	entries := dlindex.Entries(ser)
	due := 0
	for i := range eps.Items {
		e := &eps.Items[i]
		if e.Spec.SeasonNumber != ep.Spec.SeasonNumber || !itemread.OwnMonitored(e) || !itemread.Available(e, now) || !replacesAsPack(e, now) {
			continue
		}
		live := false
		for j := range entries {
			if rollup.EntryNonTerminal(&entries[j]) && !rollup.IsDonor(&entries[j]) && itemread.Covers(&entries[j], e) {
				live = true
				break
			}
		}
		if !live {
			due++
		}
	}
	return due >= 2, nil
}

// seasonBacklog reports a granted Episode that waits for its season's
// query rather than search alone (searchplan.View.SeasonBacklog): it and at
// least one more monitored, aired episode of its season, not a special,
// with no live grab hold a backlog grant, and no grab holds the season --
// the test seasonFacts' BacklogDue applies, so the Series asks for the
// season while they wait, and its answer is each one's search.
func (s *Stage) seasonBacklog(ctx context.Context, ser *catalogv1alpha1.Series, ep *catalogv1alpha1.Episode, prof *quality.Profile, now time.Time) (bool, error) {
	if ep.Spec.SeasonNumber == 0 || !ptr.Deref(ser.Spec.Monitored, true) || seasonDownloading(ser, ep.Spec.SeasonNumber, prof) {
		return false, nil
	}
	var eps catalogv1alpha1.EpisodeList
	if err := s.o.Reader.List(ctx, &eps, client.InNamespace(ser.Namespace), client.MatchingFields{series.EpisodeBySeriesRefIndex: ser.Name}); err != nil {
		return false, fmt.Errorf("search: list the Episodes of %s: %w", ser.Name, err)
	}
	entries := dlindex.Entries(ser)
	due := 0
	for i := range eps.Items {
		e := &eps.Items[i]
		if e.Spec.SeasonNumber != ep.Spec.SeasonNumber || !itemread.OwnMonitored(e) || !itemread.Available(e, now) {
			continue
		}
		if _, granted := s.o.Backlog.Granted(string(e.UID), now); !granted {
			continue
		}
		if cand, _ := wantedcron.CandidateOf(e, now); cand.Reason == "" {
			continue // imported since its grant: wanted no longer
		}
		live := false
		for j := range entries {
			if rollup.EntryNonTerminal(&entries[j]) && !rollup.IsDonor(&entries[j]) && itemread.Covers(&entries[j], e) {
				live = true
				break
			}
		}
		if !live {
			due++
		}
	}
	return due >= 2, nil
}

// seriesOriginal is ser's original language tag, "" unknown.
func seriesOriginal(ser *catalogv1alpha1.Series) string {
	if ser.Status.Metadata == nil {
		return ""
	}
	return ser.Status.Metadata.OriginalLanguage
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
// dispatches from every Series' status.seasonSearch.dispatch and every
// Comic's status.volumeSearch.dispatch (seq > answeredSeq) in the cache,
// after a leader change (series search design §4.5; 2026-11-10 design
// D10). The Search controller's interactive tasks on the same durable stay
// unledgered.
type SeasonLedgerSource struct{}

var _ dispatch.KeyedSource = SeasonLedgerSource{}

// InFlight implements dispatch.KeyedSource: k's Series' seasonSearch or
// Comic's volumeSearch dispatch, read from the cache.
func (SeasonLedgerSource) InFlight(ctx context.Context, r client.Reader, k dispatch.Key) (int64, bool, error) {
	var obj client.Object
	switch k.Kind {
	case "Series":
		obj = &catalogv1alpha1.Series{}
	case "Comic":
		obj = &catalogv1alpha1.Comic{}
	default:
		return 0, false, nil
	}
	// Read only: no copy of its status.
	if err := r.Get(ctx, client.ObjectKey{Namespace: k.Namespace, Name: k.Name}, obj, client.UnsafeDisableDeepCopy); err != nil {
		if apierrors.IsNotFound(err) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("search: read %s for the ledger audit: %w", k, err)
	}
	st := itemread.ContainerSearch(obj)
	if st == nil || !st.Dispatch.InFlight() {
		return 0, false, nil
	}
	return st.Dispatch.Seq, true, nil
}

// Durable implements dispatch.Source.
func (SeasonLedgerSource) Durable() string { return searchplan.SeasonDurable }

// Rebuild implements dispatch.Source.
func (SeasonLedgerSource) Rebuild(ctx context.Context, r client.Reader) ([]dispatch.Outstanding, error) {
	var list catalogv1alpha1.SeriesList
	// Only the series whose season dispatch is in flight, through the index.
	if err := r.List(ctx, &list, client.MatchingFields{itemread.SeasonSearchInFlightIndex: itemread.IndexTrue}); err != nil {
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
	// The Comics' volume dispatches, through the same in-flight marker
	// index (itemread.SeasonSearchInFlightValues reads a Comic's
	// volumeSearch; the loop registers it on the kind, app/remediation
	// /indexes.go), so a rebuild lists only the Comics with one open.
	var comics catalogv1alpha1.ComicList
	if err := r.List(ctx, &comics, client.MatchingFields{itemread.SeasonSearchInFlightIndex: itemread.IndexTrue}); err != nil {
		return nil, fmt.Errorf("search: rebuild the volume ledger: %w", err)
	}
	for i := range comics.Items {
		co := &comics.Items[i]
		st := co.Status.VolumeSearch
		if st == nil || !st.Dispatch.InFlight() {
			continue
		}
		out = append(out, dispatch.Outstanding{
			Key: dispatch.Key{Kind: "Comic", Namespace: co.Namespace, Name: co.Name, Sub: history.DispatchSubSeasonSearch},
			Seq: st.Dispatch.Seq,
		})
	}
	return out, nil
}

// anyPackFirst reports a container with a pack-first season: a Series one
// of whose seasons passes PackFirst, a Comic passing VolumePackFirst.
func anyPackFirst(o client.Object) bool {
	switch t := o.(type) {
	case *catalogv1alpha1.Series:
		for _, s := range t.Status.Seasons {
			if searchplan.PackFirst(s) {
				return true
			}
		}
	case *catalogv1alpha1.Comic:
		return searchplan.VolumePackFirst(t)
	}
	return false
}
