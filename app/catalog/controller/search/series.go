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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	"github.com/mediactl/clustarr/app/catalog/seriessearch"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// The planned Search's Ready reasons and Events (series search design
// §4.6, §11).
const (
	reasonQueued      = "Queued"
	reasonSearching   = "Searching"
	reasonSeriesGone  = "SeriesGone"
	reasonStalled     = "Stalled"
	eventQueued       = "SeriesSearchQueued"
	eventStarted      = "SeriesSearchStarted"
	eventFinished     = "SeriesSearchFinished"
	eventStalled      = "SeriesSearchStalled"
	plannedRequeueMax = time.Minute
)

// reconcileSeriesSearch renders a planned Search -- a series or season
// search with grabBest, which the Series key plans -- from its Series'
// status.seasonSearch (series search design §4.6). It is the sole writer
// of the Search's status (catalog): phase Running with startedAt; Ready
// False Queued while another request is served, Searching while its own
// is; status.seasons copied from the Series, each with the grabs that came
// of its answer; Completed with finishedAt once the Series' state for it
// finishes (the handshake the Series waits for before it serves the next
// request); Failed SeriesGone or Stalled. It publishes nothing and
// incorporates no record: its grabs are the Series' own.
func (r *Reconciler) reconcileSeriesSearch(ctx context.Context, s *catalogv1alpha1.Search) (ctrl.Result, error) {
	switch s.Status.Phase {
	case catalogv1alpha1.SearchPhaseCompleted, catalogv1alpha1.SearchPhaseFailed:
		return ctrl.Result{RequeueAfter: r.ttlRequeue(s)}, nil
	}
	now := r.now()
	u := newStatusUpdate(s)
	if u.phase == "" {
		at := metav1.NewTime(now)
		u.phase, u.startedAt = catalogv1alpha1.SearchPhaseRunning, &at
	}

	var ser catalogv1alpha1.Series
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Spec.MediaRef.Name}, &ser); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		return r.fail(ctx, s, reasonSeriesGone, fmt.Sprintf("the series %s no longer exists", s.Spec.MediaRef.Name))
	}
	prevReason := ""
	if c := k8s.FindCondition(s.Status.Conditions, k8s.ConditionReady); c != nil {
		prevReason = c.Reason
	}
	started := s.Status.StartedAt
	if started == nil {
		started = u.startedAt
	}

	st := ser.Status.SeasonSearch
	if st == nil || st.Request.UID != string(s.UID) {
		// Not served yet: another request is, or the Series has not taken
		// this one. A Series that never takes it stalls it.
		msg := "waiting for the series to take the search"
		if st != nil && st.FinishedAt == nil {
			msg = fmt.Sprintf("waiting for the series' search %s to finish", st.Request.Name)
		}
		if (st == nil || st.FinishedAt != nil) && now.Sub(started.Time) > searchplan.SeriesSearchStallTimeout {
			return r.stalled(ctx, s, "the series has not taken the search")
		}
		k8s.MarkFalse(s, &u.conditions, catalogv1alpha1.SearchConditionCompleted, reasonQueued, "%s", msg)
		k8s.MarkFalse(s, &u.conditions, k8s.ConditionReady, reasonQueued, "%s", msg)
		if err := r.apply(ctx, s, u); err != nil {
			return ctrl.Result{}, err
		}
		if prevReason != reasonQueued {
			r.event(s, eventQueued, "%s", msg)
		}
		return ctrl.Result{RequeueAfter: plannedRequeueMax}, nil
	}

	// Served: mirror the Series' seasons.
	u.seasons = r.mirrorSeasons(ctx, s, &ser, st)
	if st.FinishedAt != nil {
		fin := *st.FinishedAt
		u.phase, u.finishedAt = catalogv1alpha1.SearchPhaseCompleted, &fin
		msg := summarize(u.seasons)
		k8s.MarkTrue(s, &u.conditions, catalogv1alpha1.SearchConditionCompleted, k8s.ReasonReconciled, "%s", msg)
		k8s.MarkFalse(s, &u.conditions, catalogv1alpha1.SearchConditionFailed, k8s.ReasonReconciled, "search completed")
		k8s.MarkTrue(s, &u.conditions, k8s.ConditionReady, k8s.ReasonReconciled, "status.seasons reflects the series' search")
		if err := r.apply(ctx, s, u); err != nil {
			return ctrl.Result{}, err
		}
		r.event(s, eventFinished, "%s", msg)
		// The apply wakes the Search again, which then waits out its TTL.
		return ctrl.Result{}, nil
	}
	if now.Sub(lastTransition(st)) > searchplan.SeriesSearchStallTimeout {
		return r.stalled(ctx, s, "the series' search shows no progress")
	}
	k8s.MarkFalse(s, &u.conditions, catalogv1alpha1.SearchConditionCompleted, reasonSearching, "the series is searching its seasons")
	k8s.MarkFalse(s, &u.conditions, k8s.ConditionReady, reasonSearching, "the series is searching its seasons")
	if err := r.apply(ctx, s, u); err != nil {
		return ctrl.Result{}, err
	}
	if prevReason != reasonSearching {
		r.event(s, eventStarted, "the series took the search: %d seasons", len(st.Seasons))
	}
	return ctrl.Result{RequeueAfter: plannedRequeueMax}, nil
}

// stalled fails a planned Search whose served state shows no transition
// for searchplan.SeriesSearchStallTimeout: the Search fails, not the
// Series, whose state finishes on its own clock.
func (r *Reconciler) stalled(ctx context.Context, s *catalogv1alpha1.Search, why string) (ctrl.Result, error) {
	msg := fmt.Sprintf("%s for %s", why, searchplan.SeriesSearchStallTimeout)
	r.eventWarning(s, eventStalled, "%s", msg)
	return r.fail(ctx, s, reasonStalled, msg)
}

// lastTransition is the newest instant the state moved: started, a season
// answered, or a season dispatched.
func lastTransition(st *catalogv1alpha1.SeasonSearchState) time.Time {
	last := st.StartedAt.Time
	for _, se := range st.Seasons {
		if se.AnsweredAt != nil && se.AnsweredAt.After(last) {
			last = se.AnsweredAt.Time
		}
	}
	if d := st.Dispatch; d != nil && d.DispatchedAt.After(last) {
		last = d.DispatchedAt.Time
	}
	return last
}

// mirrorSeasons is status.seasons from the Series' state: each season's
// phase, reason and counts, and grabbed -- the Series' entries whose
// release is among the season answer's approved candidates, grabbed at or
// after the answer -- never fewer than already reported (an imported
// entry leaves status.downloads).
func (r *Reconciler) mirrorSeasons(ctx context.Context, s *catalogv1alpha1.Search, ser *catalogv1alpha1.Series, st *catalogv1alpha1.SeasonSearchState) []catalogv1alpha1.SearchSeason {
	prev := map[int32]int32{}
	for _, se := range s.Status.Seasons {
		prev[se.Number] = se.Grabbed
	}
	out := make([]catalogv1alpha1.SearchSeason, 0, len(st.Seasons))
	for _, se := range st.Seasons {
		ss := catalogv1alpha1.SearchSeason{
			Number: se.Number, Phase: se.Phase, Reason: se.Reason,
			Results: se.Candidates, Approved: se.Approved, Grabbed: prev[se.Number],
		}
		if se.Phase == catalogv1alpha1.SeasonSearchAnswered && se.AnsweredAt != nil && r.Searches != nil {
			rec, _, ok, err := r.Searches.Get(ctx, agentrecords.SeasonSearchKey(string(ser.UID), se.Number))
			if err != nil {
				logging.FromContext(ctx).Warn("search: read a season record", "season", se.Number, "err", err)
			} else if ok && rec.Seq == se.Seq {
				approved := map[string]bool{}
				for i := range rec.Candidates {
					if c := &rec.Candidates[i]; c.Decision.Approved {
						approved[c.Decision.GUID] = true
					}
				}
				var n int32
				for i := range ser.Status.Downloads {
					e := &ser.Status.Downloads[i]
					if approved[e.Release.GUID] && !e.GrabbedAt.Before(se.AnsweredAt) {
						n++
					}
				}
				ss.Grabbed = max(ss.Grabbed, n)
			}
		}
		out = append(out, ss)
	}
	return out
}

// summarize is a finished planned Search's message: "searched 3 seasons,
// skipped 2, grabbed 2 releases".
func summarize(seasons []catalogv1alpha1.SearchSeason) string {
	var searched, skipped, failed, grabbed int
	for _, se := range seasons {
		switch se.Phase {
		case catalogv1alpha1.SeasonSearchAnswered:
			searched++
		case catalogv1alpha1.SeasonSearchSkipped:
			skipped++
		case catalogv1alpha1.SeasonSearchFailed, catalogv1alpha1.SeasonSearchCanceled:
			failed++
		}
		grabbed += int(se.Grabbed)
	}
	msg := fmt.Sprintf("searched %d seasons, skipped %d, grabbed %d releases", searched, skipped, grabbed)
	if failed > 0 {
		msg += fmt.Sprintf("; %d failed or canceled", failed)
	}
	return msg
}

// searchesOfSeries maps a Series to its planned Searches (the watch): a
// change of its season search moves theirs.
func (r *Reconciler) searchesOfSeries(ctx context.Context, o client.Object) []reconcile.Request {
	list, err := seriessearch.Of(ctx, r.Client, o.GetNamespace(), o.GetName())
	if err != nil {
		logging.FromContext(ctx).Warn("search: list a series' planned Searches", "series", o.GetName(), "err", err)
		return nil
	}
	out := make([]reconcile.Request, 0, len(list))
	for i := range list {
		out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list[i])})
	}
	return out
}
