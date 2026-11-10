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

// Package backlogsearch is the BacklogSearch controller
// (docs/superpowers/specs/2026-11-10-backlog-search-design.md): every
// spec.interval it lists its namespace's backlog through
// itemread.BacklogIndex, chooses the searches a cycle starts
// (backlog.Select), grants them in the leader's book and wakes their keys,
// where the search planner dispatches each as a live search behind the
// namespace's pace and the dispatch ledger. It writes nothing but its own
// status, under catalog-backlog.
package backlogsearch

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/backlog"
	"github.com/mediactl/clustarr/app/catalog/controller/wantedcron"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// GrantLapse is a grant's least life: longer than the automatic season
// search's one-hour cooldown, which a season unit may wait out.
const GrantLapse = 3 * time.Hour

// Reconciler is the BacklogSearch controller.
type Reconciler struct {
	// Client reads through the manager's cache: the backlog through
	// itemread.BacklogIndex, never a whole kind.
	Client client.Client
	// Book is the leader's grant book the search stage reads.
	Book *backlog.Book
	// Clock is the controller's clock; nil is time.Now.
	Clock func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock().UTC()
	}
	return time.Now().UTC()
}

// Reconcile runs a cycle when one is due and requeues at the next.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "backlogsearch.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("backlogsearch", req.NamespacedName)

	var bs catalogv1alpha1.BacklogSearch
	if err := r.Client.Get(ctx, req.NamespacedName, &bs); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if bs.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}
	now := r.now()
	st := bs.Status.DeepCopy()
	st.ObservedGeneration = bs.Generation

	winner, err := r.winner(ctx, bs.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	switch {
	case winner != bs.Name:
		mark(&bs, st, false, catalogv1alpha1.BacklogSearchReasonOverlap,
			fmt.Sprintf("BacklogSearch %s searches this namespace's backlog; this one searches none", winner))
		st.NextCycleAt = nil
		return ctrl.Result{}, r.apply(ctx, &bs, st)
	case !bs.Spec.IsEnabled():
		mark(&bs, st, false, catalogv1alpha1.BacklogSearchReasonPaused, "spec.enabled is false")
		st.NextCycleAt = nil
		return ctrl.Result{}, r.apply(ctx, &bs, st)
	}

	interval := bs.Spec.IntervalOrDefault()
	if st.LastCycleAt != nil {
		if next := st.LastCycleAt.Add(interval); now.Before(next) {
			// Not due: a spec edit or a restart. The status keeps its clock.
			if st.NextCycleAt == nil || !st.NextCycleAt.Time.Equal(next) || bs.Status.ObservedGeneration != bs.Generation {
				t := metav1.NewTime(next)
				st.NextCycleAt = &t
				mark(&bs, st, true, catalogv1alpha1.BacklogSearchReasonActive, readyMessage(st))
				if err := r.apply(ctx, &bs, st); err != nil {
					return ctrl.Result{}, err
				}
			}
			return ctrl.Result{RequeueAfter: next.Sub(now)}, nil
		}
	}

	units, err := r.cycle(ctx, &bs, st, now)
	if err != nil {
		return ctrl.Result{}, err
	}
	mark(&bs, st, true, catalogv1alpha1.BacklogSearchReasonActive, readyMessage(st))
	if err := r.apply(ctx, &bs, st); err != nil {
		return ctrl.Result{}, err
	}
	log.Info("backlog search: cycle", "started", len(units), "lastHour", st.SearchesLastHour,
		"missingDue", queueDue(st.Missing), "cutoffUnmetDue", queueDue(st.CutoffUnmet))
	return ctrl.Result{RequeueAfter: interval}, nil
}

// winner is the namespace's BacklogSearch that runs: the oldest, then the
// first by name.
func (r *Reconciler) winner(ctx context.Context, namespace string) (string, error) {
	var l catalogv1alpha1.BacklogSearchList
	if err := r.Client.List(ctx, &l, client.InNamespace(namespace)); err != nil {
		return "", fmt.Errorf("backlogsearch: list: %w", err)
	}
	var live []*catalogv1alpha1.BacklogSearch
	for i := range l.Items {
		if l.Items[i].DeletionTimestamp == nil {
			live = append(live, &l.Items[i])
		}
	}
	if len(live) == 0 {
		return "", nil
	}
	return slices.MinFunc(live, func(a, b *catalogv1alpha1.BacklogSearch) int {
		return cmp.Or(a.CreationTimestamp.Compare(b.CreationTimestamp.Time), cmp.Compare(a.Name, b.Name))
	}).Name, nil
}

// cycle lists the backlog, chooses, grants and wakes, and fills st.
func (r *Reconciler) cycle(ctx context.Context, bs *catalogv1alpha1.BacklogSearch, st *catalogv1alpha1.BacklogSearchStatus, now time.Time) ([]backlog.Unit, error) {
	cands, err := r.candidates(ctx, bs, now)
	if err != nil {
		return nil, err
	}
	used := lastHour(st.Recent, now)
	cfg := backlog.Config{
		Queues: map[schema.SearchReason]backlog.Queue{
			schema.SearchReasonMissing:     {PerCycle: int(bs.Spec.MissingPerCycle()), Cooldown: bs.Spec.MissingCooldown()},
			schema.SearchReasonCutoffUnmet: {PerCycle: int(bs.Spec.CutoffUnmetPerCycle()), Cooldown: bs.Spec.CutoffUnmetCooldown()},
		},
		Room:         max(int(bs.Spec.HourlyLimitOrDefault())-used, 0),
		SeasonSearch: bs.Spec.SeasonSearchOrDefault(),
	}
	units, counts := backlog.Select(cands, cfg, now)

	until := now.Add(max(2*bs.Spec.IntervalOrDefault(), GrantLapse))
	at := metav1.NewTime(now.Truncate(time.Second))
	var started []catalogv1alpha1.BacklogSearchUnit
	for _, u := range units {
		for _, c := range u.Items {
			r.Book.Grant(c.Item.UID, backlog.Grant{Reason: c.Reason, At: now, Until: until})
		}
		// The Series first, so its season facts count the grants before its
		// episodes plan.
		if u.Series != nil {
			if err := r.Book.Wake(ctx, *u.Series); err != nil {
				return units, err
			}
		}
		for _, c := range u.Items {
			if err := r.Book.Wake(ctx, c.Item); err != nil {
				return units, err
			}
		}
		started = append(started, unitStatus(u, at))
		kind := "item"
		if u.Season != nil {
			kind = "season"
		}
		metrics.BacklogSearchesTotal.WithLabelValues(string(u.Reason), kind).Inc()
	}

	// Newest first, then the older ones, at most MaxBacklogRecent.
	st.Recent = append(started, st.Recent...)
	if len(st.Recent) > catalogv1alpha1.MaxBacklogRecent {
		st.Recent = st.Recent[:catalogv1alpha1.MaxBacklogRecent]
	}
	st.SearchesLastHour = int32(used + len(started))
	st.LastCycleAt = &at
	next := metav1.NewTime(now.Add(bs.Spec.IntervalOrDefault()).Truncate(time.Second))
	st.NextCycleAt = &next
	st.Missing = queueStatus(counts[schema.SearchReasonMissing])
	st.CutoffUnmet = queueStatus(counts[schema.SearchReasonCutoffUnmet])
	for _, reason := range backlog.Reasons {
		c := counts[reason]
		metrics.BacklogItems.WithLabelValues(string(reason), "candidate").Set(float64(c.Candidates))
		metrics.BacklogItems.WithLabelValues(string(reason), "due").Set(float64(c.Due))
	}
	return units, nil
}

// searchedKinds are the kinds a backlog searches, in the order a cycle lists
// them.
var searchedKinds = []commonv1.MediaKind{
	commonv1.MediaKindMovie, commonv1.MediaKindEpisode, commonv1.MediaKindAlbum,
	commonv1.MediaKindBook, commonv1.MediaKindAudiobook, commonv1.MediaKindIssue,
}

// candidates are the namespace's backlog items as a cycle reads them:
// listed by reason through the index, judged again at now (an Issue's
// cover date, a stale index value), searched before, nothing in flight,
// monitored with their container (a Series, Comic, Artist or Author).
func (r *Reconciler) candidates(ctx context.Context, bs *catalogv1alpha1.BacklogSearch, now time.Time) ([]backlog.Candidate, error) {
	containers := map[string]container{} // read once per cycle: every Get decodes a row
	var out []backlog.Candidate
	for _, kind := range searchedKinds {
		if !bs.Spec.Searches(kind) {
			continue
		}
		for _, reason := range backlog.Reasons {
			items, err := r.list(ctx, kind, bs.Namespace, reason)
			if err != nil {
				return nil, err
			}
			for _, o := range items {
				c, ok, err := r.candidate(ctx, o, reason, now, containers)
				if err != nil {
					return nil, err
				}
				if ok {
					out = append(out, c)
				}
			}
		}
	}
	return out, nil
}

// list is kind's items in namespace whose backlog index reads reason.
func (r *Reconciler) list(ctx context.Context, kind commonv1.MediaKind, namespace string, reason schema.SearchReason) ([]client.Object, error) {
	opts := []client.ListOption{client.InNamespace(namespace), client.MatchingFields{itemread.BacklogIndex: string(reason)}}
	var out []client.Object
	switch kind {
	case commonv1.MediaKindMovie:
		var l catalogv1alpha1.MovieList
		if err := r.Client.List(ctx, &l, opts...); err != nil {
			return nil, fmt.Errorf("backlogsearch: list movies: %w", err)
		}
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
	case commonv1.MediaKindEpisode:
		var l catalogv1alpha1.EpisodeList
		if err := r.Client.List(ctx, &l, opts...); err != nil {
			return nil, fmt.Errorf("backlogsearch: list episodes: %w", err)
		}
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
	case commonv1.MediaKindAlbum:
		var l catalogv1alpha1.AlbumList
		if err := r.Client.List(ctx, &l, opts...); err != nil {
			return nil, fmt.Errorf("backlogsearch: list albums: %w", err)
		}
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
	case commonv1.MediaKindBook:
		var l catalogv1alpha1.BookList
		if err := r.Client.List(ctx, &l, opts...); err != nil {
			return nil, fmt.Errorf("backlogsearch: list books: %w", err)
		}
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
	case commonv1.MediaKindAudiobook:
		var l catalogv1alpha1.AudiobookList
		if err := r.Client.List(ctx, &l, opts...); err != nil {
			return nil, fmt.Errorf("backlogsearch: list audiobooks: %w", err)
		}
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
	case commonv1.MediaKindIssue:
		var l catalogv1alpha1.IssueList
		if err := r.Client.List(ctx, &l, opts...); err != nil {
			return nil, fmt.Errorf("backlogsearch: list issues: %w", err)
		}
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
	}
	return out, nil
}

// candidate reads one listed item, false when it is no candidate at now.
func (r *Reconciler) candidate(ctx context.Context, o client.Object, reason schema.SearchReason, now time.Time, containers map[string]container) (backlog.Candidate, bool, error) {
	c, ok := wantedcron.CandidateOf(o, now)
	if !ok || c.Reason != reason || (c.Attempts.Count == 0 && c.Attempts.Latest == nil) {
		return backlog.Candidate{}, false, nil
	}
	if itemread.SearchDispatch(o).InFlight() || !itemread.OwnMonitored(o) {
		return backlog.Candidate{}, false, nil
	}
	ref := itemread.Ref(o)
	cand := backlog.Candidate{Item: ref, Reason: reason}
	if t := itemread.LastLiveSearchAt(o); t != nil {
		at := t.UTC()
		cand.LastLive = &at
	}
	if a := c.Attempts.Latest; a != nil {
		at := a.UTC()
		cand.LastSearched = &at
	}
	switch t := o.(type) {
	case *catalogv1alpha1.Episode:
		ser, err := r.container(ctx, &catalogv1alpha1.Series{}, t.Namespace, t.Spec.SeriesRef, containers)
		if err != nil || !ser.monitored {
			return backlog.Candidate{}, false, err
		}
		sref := ser.ref
		season := t.Spec.SeasonNumber
		cand.Series, cand.Season = &sref, &season
	case *catalogv1alpha1.Issue:
		co, err := r.container(ctx, &catalogv1alpha1.Comic{}, t.Namespace, t.Spec.ComicRef, containers)
		if err != nil || !co.monitored {
			return backlog.Candidate{}, false, err
		}
	case *catalogv1alpha1.Album:
		// Monitoring cascades from the Artist (2026-11-10 design D3), as
		// itemread.Monitored reads it for the search planner.
		ar, err := r.container(ctx, &catalogv1alpha1.Artist{}, t.Namespace, t.Spec.ArtistRef, containers)
		if err != nil || !ar.monitored {
			return backlog.Candidate{}, false, err
		}
	case *catalogv1alpha1.Book:
		// A standalone Book (no authorRef) is governed by its own flag alone.
		if name := ptr.Deref(t.Spec.AuthorRef, ""); name != "" {
			au, err := r.container(ctx, &catalogv1alpha1.Author{}, t.Namespace, name, containers)
			if err != nil || !au.monitored {
				return backlog.Candidate{}, false, err
			}
		}
	}
	return cand, true, nil
}

// container is a Series, Comic, Artist or Author as a cycle needs it.
type container struct {
	monitored bool
	ref       schema.ItemRef
}

// container reads a Series or Comic once per cycle; one gone is
// unmonitored.
func (r *Reconciler) container(ctx context.Context, obj client.Object, namespace, name string, memo map[string]container) (container, error) {
	key := fmt.Sprintf("%T/%s", obj, name)
	if c, ok := memo[key]; ok {
		return c, nil
	}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, obj); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return container{}, err
		}
		memo[key] = container{}
		return container{}, nil
	}
	c := container{monitored: itemread.OwnMonitored(obj), ref: itemread.Ref(obj)}
	memo[key] = c
	return c, nil
}

// lastHour counts the recent searches started in the hour before now.
func lastHour(recent []catalogv1alpha1.BacklogSearchUnit, now time.Time) int {
	n := 0
	for _, u := range recent {
		if now.Sub(u.At.Time) < time.Hour {
			n++
		}
	}
	return n
}

func unitStatus(u backlog.Unit, at metav1.Time) catalogv1alpha1.BacklogSearchUnit {
	out := catalogv1alpha1.BacklogSearchUnit{Reason: string(u.Reason), At: at, Items: int32(len(u.Items))}
	if u.Series != nil && u.Season != nil {
		s := *u.Season
		out.Kind, out.Name, out.Season = commonv1.MediaKindSeries, u.Series.Name, &s
		return out
	}
	c := u.Items[0]
	out.Kind, out.Name = commonv1.MediaKind(strings.ToLower(c.Item.Kind)), c.Item.Name
	return out
}

func queueStatus(c backlog.Counts) *catalogv1alpha1.BacklogQueueStatus {
	q := &catalogv1alpha1.BacklogQueueStatus{Candidates: int32(c.Candidates), Due: int32(c.Due)}
	if c.OldestLive != nil {
		t := metav1.NewTime(*c.OldestLive)
		q.OldestLiveSearchAt = &t
	}
	return q
}

func queueDue(q *catalogv1alpha1.BacklogQueueStatus) int32 {
	if q == nil {
		return 0
	}
	return q.Due
}

func readyMessage(st *catalogv1alpha1.BacklogSearchStatus) string {
	return "due: " + strconv.Itoa(int(queueDue(st.Missing))) + " missing, " +
		strconv.Itoa(int(queueDue(st.CutoffUnmet))) + " below cutoff; " +
		strconv.Itoa(int(st.SearchesLastHour)) + " searches in the last hour"
}

// mark sets st's Ready condition.
func mark(bs *catalogv1alpha1.BacklogSearch, st *catalogv1alpha1.BacklogSearchStatus, ready bool, reason, message string) {
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	k8s.SetCondition(bs, &st.Conditions, metav1.Condition{
		Type: catalogv1alpha1.BacklogSearchConditionReady, Status: status,
		Reason: reason, Message: message, ObservedGeneration: bs.Generation,
	})
}

// apply writes st whole under catalog-backlog: every field the manager
// owns, each time (CLAUDE.md, server-side apply).
func (r *Reconciler) apply(ctx context.Context, bs *catalogv1alpha1.BacklogSearch, st *catalogv1alpha1.BacklogSearchStatus) error {
	sac := catalogac.BacklogSearchStatus().
		WithObservedGeneration(st.ObservedGeneration).
		WithConditions(k8s.ConditionACs(st.Conditions)...).
		WithSearchesLastHour(st.SearchesLastHour)
	if st.LastCycleAt != nil {
		sac.WithLastCycleAt(*st.LastCycleAt)
	}
	if st.NextCycleAt != nil {
		sac.WithNextCycleAt(*st.NextCycleAt)
	}
	if q := st.Missing; q != nil {
		sac.WithMissing(queueAC(q))
	}
	if q := st.CutoffUnmet; q != nil {
		sac.WithCutoffUnmet(queueAC(q))
	}
	for _, u := range st.Recent {
		uac := catalogac.BacklogSearchUnit().WithKind(u.Kind).WithName(u.Name).WithItems(u.Items).WithReason(u.Reason).WithAt(u.At)
		if u.Season != nil {
			uac.WithSeason(*u.Season)
		}
		sac.WithRecent(uac)
	}
	ac := catalogac.BacklogSearch(bs.Name, bs.Namespace).WithStatus(sac)
	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogBacklog, ac); err != nil {
		return fmt.Errorf("backlogsearch: status of %s: %w", bs.Name, err)
	}
	return nil
}

func queueAC(q *catalogv1alpha1.BacklogQueueStatus) *catalogac.BacklogQueueStatusApplyConfiguration {
	ac := catalogac.BacklogQueueStatus().WithCandidates(q.Candidates).WithDue(q.Due)
	if q.OldestLiveSearchAt != nil {
		ac.WithOldestLiveSearchAt(*q.OldestLiveSearchAt)
	}
	return ac
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=backlogsearches,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=backlogsearches/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;episodes;albums;books;audiobooks;issues;series;comics;artists;authors,verbs=get;list;watch

// SetupWithManager registers the controller. A status write changes no
// generation, so it does not wake the controller; its RequeueAfter is the
// cycle clock.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("backlogsearch").
		For(&catalogv1alpha1.BacklogSearch{}, builder.WithPredicates(k8s.GenerationChanged())).
		WithOptions(controller.Options{ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}
