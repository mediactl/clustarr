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
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/app/catalog/searchoutcome"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/app/remediation/dlindex"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/records/recordsource"
	"github.com/mediactl/clustarr/pkg/version"
)

// MaxResults is the hard cap on Search.status.results
// (+kubebuilder:validation:MaxItems=200).
const MaxResults = 200

// maxRejections is ReleaseDecision.Rejections' MaxItems.
const maxRejections = 20

// pickRequeue is how often a Search looks again for the entry its pick
// became, or the owner's refusal of it.
const pickRequeue = 5 * time.Second

// incorporateAttempts bounds the compare-and-swap of an interactive answer.
const incorporateAttempts = 5

// taskSeq is an interactive Search's task seq: its generation, so an edit
// that starts it again answers anew.
func taskSeq(s *catalogv1alpha1.Search) int64 { return max(1, s.Generation) }

// incorporate writes an interactive search's answer into its status
// (ADR-0019 §7.4): finishedAt, indexerOutcomes and results under
// catalog-worker, by compare-and-swap (W3's fence), each result carrying
// the item-state rejections of the target's fresh state (R24). It reports
// whether it wrote.
func (r *Reconciler) incorporate(ctx context.Context, s *catalogv1alpha1.Search) (bool, error) {
	if r.Searches == nil || s.Spec.MediaRef == nil {
		return false, nil
	}
	rec, _, ok, err := r.Searches.Get(ctx, agentrecords.SearchKey(string(s.UID)))
	if err != nil {
		return false, fmt.Errorf("read the search record: %w", err)
	}
	if !ok || rec.Seq < taskSeq(s) {
		return false, nil
	}
	var (
		results []commonv1.ReleaseDecision
		other   int32
	)
	if s.Spec.Season != nil {
		// An interactive season search (Wave S): each result judged as the
		// Series' grab planner would judge it.
		results, other = r.seasonItemState(ctx, s, rec.Candidates)
	} else {
		results, other = r.withItemState(ctx, s, rec.Candidates)
	}
	outcomes := searchoutcome.Map(rec.IndexerOutcomes)
	finished := metav1.NewTime(rec.FinishedAt.UTC())
	_, applied, err := k8s.PatchStatusCAS(ctx, r.apiReader(), r.Client, k8s.ManagerCatalogWorker,
		client.ObjectKeyFromObject(s), func() *catalogv1alpha1.Search { return &catalogv1alpha1.Search{} },
		func(fresh *catalogv1alpha1.Search) (*catalogac.SearchApplyConfiguration, bool, error) {
			if fresh.Status.FinishedAt != nil && !fresh.Status.FinishedAt.Before(&finished) {
				return nil, true, nil
			}
			oacs, err := k8s.ApplyConfigurationsFrom[catalogac.IndexerOutcomeApplyConfiguration](outcomes)
			if err != nil {
				return nil, false, err
			}
			st := catalogac.SearchStatus().WithFinishedAt(finished).WithIndexerOutcomes(oacs...).WithResults(results...).
				WithOtherTitles(other)
			return catalogac.Search(fresh.Name, fresh.Namespace).WithStatus(st), false, nil
		}, incorporateAttempts)
	if err == nil && applied {
		countAnswer(s, rec, results)
	}
	return applied, err
}

// countAnswer counts an incorporated interactive answer: its results by
// band, and an interactive season search's season.
func countAnswer(s *catalogv1alpha1.Search, rec *schema.SearchRecord, results []commonv1.ReleaseDecision) {
	for i := range results {
		metrics.SearchResultsTotal.WithLabelValues(decision.BandOf(results[i].Approved, results[i].Rejections).String()).Inc()
	}
	if s.Spec.Season == nil {
		return
	}
	outcome := "answered"
	switch {
	case rec.State == records.StateFailed:
		outcome = "failed"
	case rec.AllPaced:
		outcome = "paced"
	}
	metrics.SeasonSearchesTotal.WithLabelValues("interactive", outcome).Inc()
}

// withItemState is an interactive answer's results for status: the
// release decisions the search agent ranked, each with the rejections the
// target item's fresh state adds (R24: the queue, the current file and its
// cutoff, TranscodedFinal, the blocklist), banded approved, rejected, other
// titles (Wave S) and capped at the Search's keep limit; the second result
// is how many of them, at their end, are other titles.
func (r *Reconciler) withItemState(ctx context.Context, s *catalogv1alpha1.Search, cands []schema.ScoredRelease) ([]commonv1.ReleaseDecision, int32) {
	limit := keepLimit(s)
	target, profile := r.itemTarget(ctx, s)
	out := make([]commonv1.ReleaseDecision, 0, len(cands))
	for _, sr := range cands {
		rd := *sr.Decision.DeepCopy()
		if profile != nil {
			t := target
			blocked := sr.Blocked != nil
			t.Blocklist = func(string, string) bool { return blocked }
			extra := decision.ItemStateRejections(ctx, t, *profile,
				decision.Decision{Release: rd.ReleaseInfo, Score: int(sr.Score)}, decision.Options{UserInvoked: true})
			if len(extra) > 0 {
				rd.Rejections = append(rd.Rejections, extra...)
				if len(rd.Rejections) > maxRejections {
					rd.Rejections = rd.Rejections[:maxRejections]
				}
				rd.Approved = false
				rd.TemporarilyRejected = allTemporary(rd.Rejections)
			}
		}
		out = append(out, rd)
	}
	return banded(out, limit)
}

func allTemporary(rs []commonv1.Rejection) bool {
	for _, r := range rs {
		if r.Type != commonv1.RejectionTemporary {
			return false
		}
	}
	return len(rs) > 0
}

// keepLimit is how many ranked results to keep: Search.spec.limit, else
// MaxResults.
func keepLimit(s *catalogv1alpha1.Search) int {
	if s.Spec.Limit > 0 && s.Spec.Limit <= MaxResults {
		return int(s.Spec.Limit)
	}
	return MaxResults
}

// itemTarget is the decision.Target of a Search's item for the item-state
// rules, and its resolved profile (nil when it has none: no rule runs).
func (r *Reconciler) itemTarget(ctx context.Context, s *catalogv1alpha1.Search) (decision.Target, *quality.Profile) {
	item, ok := itemread.New(string(s.Spec.MediaRef.Kind))
	if !ok {
		return decision.Target{}, nil
	}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Spec.MediaRef.Name}, item); err != nil {
		return decision.Target{}, nil
	}
	cfg, err := itemread.ConfigOf(ctx, r.Client, item)
	if err != nil {
		return decision.Target{}, nil
	}
	profile, err := itemread.Profile(ctx, r.Client, cfg.QualityProfileRef)
	if err != nil {
		return decision.Target{}, nil
	}
	t := decision.Target{Kind: s.Spec.MediaRef.Kind, OriginalLanguageTag: itemread.OriginalLanguage(ctx, r.Client, item)}
	if cur, err := itemread.Current(ctx, r.Client, item); err == nil {
		t.Current = cur
	}
	if owner, err := itemread.Owner(ctx, r.Client, item); err == nil && owner != nil {
		t.Queue = itemread.Queue(dlindex.Entries(owner), item, "")
	}
	return t, profile
}

// handlePicks sends each pick (spec.grab, and grabBest's best approved
// result) to the item's owner as a Candidate (§6.6 step 4) and reports, in
// status.grabbed, the entry it became on the owner, the owner's refusal, or
// nothing yet (it looks again shortly).
func (r *Reconciler) handlePicks(ctx context.Context, s *catalogv1alpha1.Search) (reconcile.Result, error) {
	guids := grabGUIDs(s)
	best := bestApproved(s)
	grabbed := make(map[string]catalogv1alpha1.GrabResult, len(s.Status.Grabbed))
	for _, g := range s.Status.Grabbed {
		grabbed[g.GUID] = g
	}
	waiting := false

	if s.Spec.MediaRef == nil {
		for _, guid := range guids {
			if existing, done := grabbed[guid]; done && existing.Error == "" {
				continue
			}
			grabbed[guid] = catalogv1alpha1.GrabResult{
				GUID:  guid,
				Error: "query-mode search has no spec.mediaRef; nothing to grab it for",
			}
		}
	} else {
		item, owner, err := r.owner(ctx, s)
		if err != nil {
			return reconcile.Result{}, err
		}
		for _, guid := range guids {
			cur, have := grabbed[guid]
			if have && cur.DownloadRef != "" {
				continue
			}
			if owner != nil {
				if id := entryFor(s, owner, item, guid); id != "" {
					grabbed[guid] = catalogv1alpha1.GrabResult{GUID: guid, DownloadRef: id}
					r.event(s, "Grabbed", "the release is grab %s on %s", id, owner.GetName())
					continue
				}
			}
			if o, ok := r.settled(s, guid); ok && o == intake.Refused {
				// Sent again only under a new Msg-Id: flipping spec.override.
				grabbed[guid] = catalogv1alpha1.GrabResult{GUID: guid, Error: "the item's owner did not take the release (see its Events)"}
				continue
			}
			if have && cur.Error == "" {
				// Sent; its entry or the owner's refusal is still to come.
				waiting = true
				continue
			}
			got := resolveGrab(guid, s.Status.Results, s.Spec.Override)
			if !got.Allowed {
				grabbed[guid] = catalogv1alpha1.GrabResult{GUID: guid, Error: got.Error}
				continue
			}
			if owner == nil {
				grabbed[guid] = catalogv1alpha1.GrabResult{GUID: guid, Error: "the item to grab it for no longer exists"}
				continue
			}
			// grabBest's pick is the automatic search's grab, not a
			// person's: grabbedBy search, not manual.
			manual := guid != best || containsString(s.Spec.Grab, guid)
			if err := r.publishPick(ctx, s, item, owner, guid, manual); err != nil {
				grabbed[guid] = catalogv1alpha1.GrabResult{GUID: guid, Error: err.Error()}
				r.eventWarning(s, "GrabFailed", "could not send the pick %s: %v", guid, err)
				continue
			}
			grabbed[guid] = catalogv1alpha1.GrabResult{GUID: guid}
			waiting = true
		}
	}

	u := newStatusUpdate(s)
	u.grabbed = make([]catalogv1alpha1.GrabResult, 0, len(guids))
	for _, guid := range guids {
		u.grabbed = append(u.grabbed, grabbed[guid])
	}
	if err := r.apply(ctx, s, u); err != nil {
		return reconcile.Result{}, err
	}
	if waiting {
		return reconcile.Result{RequeueAfter: pickRequeue}, nil
	}
	return reconcile.Result{RequeueAfter: r.ttlRequeue(s)}, nil
}

// owner reads the Search's item and its grab owner (an Episode's Series, an
// Issue's Comic, else the item); nil when either is gone.
func (r *Reconciler) owner(ctx context.Context, s *catalogv1alpha1.Search) (client.Object, client.Object, error) {
	item, ok := itemread.New(string(s.Spec.MediaRef.Kind))
	if !ok {
		return nil, nil, nil
	}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Spec.MediaRef.Name}, item); err != nil {
		return nil, nil, client.IgnoreNotFound(err)
	}
	owner, err := itemread.Owner(ctx, r.Client, item)
	return item, owner, err
}

// entryFor is the id of the owner's video entry for guid covering item,
// "" for none. A Blocklisted tombstone grabbed before s was created (kept
// lifecycle.BlockQuarantine after its transfer is gone) is no grab of the
// pick: it would read "Grabbed" for a release the owner refused earlier,
// and keep a spec.override pick from ever being sent (smoke T2). A
// Blocklisted entry grabbed since is the pick's own grab, refused already
// -- a payload the engine cannot fetch is blocklisted in the second it is
// grabbed, before the Search looks again -- and without it the pick would
// wait for an entry forever (smoke re-verification 2026-10-08, e2e
// scenario 3).
func entryFor(s *catalogv1alpha1.Search, owner, item client.Object, guid string) string {
	entries := dlindex.Entries(owner)
	for i := range entries {
		e := &entries[i]
		if e.Release.GUID != guid || rollup.IsDonor(e) || !itemread.Covers(e, item) {
			continue
		}
		if e.Phase == commonv1.DownloadPhaseBlocklisted && e.GrabbedAt.Before(&s.CreationTimestamp) {
			continue
		}
		return e.ID
	}
	return ""
}

// pickMsgID is a pick's intake Msg-Id: scoped by the Search, and by
// spec.override, so flipping override sends the pick again rather than into
// the duplicate window.
func pickMsgID(s *catalogv1alpha1.Search, indexer, guid string) string {
	if s.Spec.Override {
		guid += "#override"
	}
	return events.MsgIDForCandidate(events.CandidateMsgOriginPick, string(s.UID), indexer, guid)
}

// settled is the owner's settlement of guid's pick, from the intake inbox.
func (r *Reconciler) settled(s *catalogv1alpha1.Search, guid string) (intake.Outcome, bool) {
	if r.Inbox == nil {
		return "", false
	}
	for _, res := range s.Status.Results {
		if res.GUID == guid {
			return r.Inbox.Settled(pickMsgID(s, res.IndexerRef, guid))
		}
	}
	return "", false
}

// publishPick sends one pick to the owner's candidate intake: Manual for
// spec.grab (exempt from TranscodedFinal and temporary rejections),
// Override from spec.override, grabbedBy interactive -- or, for grabBest's
// pick, an automatic search's grab, grabbedBy search.
func (r *Reconciler) publishPick(ctx context.Context, s *catalogv1alpha1.Search, item, owner client.Object, guid string, manual bool) error {
	sr, ok := r.scored(ctx, s, guid)
	if !ok {
		return fmt.Errorf("guid %s is not in the search's answer", guid)
	}
	oref := itemread.Ref(owner)
	c := schema.Candidate{
		Owner: oref, Origin: schema.CandidateOriginInteractive, SearchRef: &schema.Ref{Namespace: s.Namespace, Name: s.Name, UID: string(s.UID)},
		Manual: manual, Override: s.Spec.Override, GrabbedBy: commonv1.GrabSourceInteractive, Release: sr, At: r.now(),
	}
	if !manual {
		c.Origin, c.GrabbedBy = schema.CandidateOriginSearch, commonv1.GrabSourceSearch
	}
	if iref := itemread.Ref(item); iref.UID != oref.UID {
		c.Targets = []schema.ItemRef{iref}
	} else if s.Spec.Season != nil {
		// An interactive season search's pick (Wave S): no Targets, the
		// Series resolves what it covers from its coverage.
		n := *s.Spec.Season
		c.Season = &n
	}
	name, data, err := schema.Encode(c)
	if err != nil {
		return err
	}
	id := pickMsgID(s, sr.Decision.IndexerRef, guid)
	env := &events.Envelope{
		ID: id, Type: "catalog.Candidate", Schema: name, Source: "manager@" + version.String(),
		Key: oref.Namespace + "/" + oref.Name, Time: r.now(), Data: data,
	}
	if _, err := r.Bus.Publish(ctx, events.IntakeCandidateSubject(oref.Kind, oref.UID), env, events.WithMsgID(id)); err != nil {
		return fmt.Errorf("send the pick: %w", err)
	}
	return nil
}

// scored is guid's scored release: the search agent's answer (the release
// verdict the owner's planner re-checks), else the status result.
func (r *Reconciler) scored(ctx context.Context, s *catalogv1alpha1.Search, guid string) (schema.ScoredRelease, bool) {
	if r.Searches != nil {
		if rec, _, ok, err := r.Searches.Get(ctx, agentrecords.SearchKey(string(s.UID))); err == nil && ok {
			for _, sr := range rec.Candidates {
				if sr.Decision.GUID == guid {
					return sr, true
				}
			}
		}
	}
	for _, res := range s.Status.Results {
		if res.GUID == guid {
			return schema.ScoredRelease{Decision: res, Score: res.FormatScore}, true
		}
	}
	return schema.ScoredRelease{}, false
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// recordSource is the Search controller's source on clustarr-searches: a
// record whose item is a Search wakes it.
func recordSource(bus recordsource.Bus) *recordsource.Source[reconcile.Request] {
	return recordsource.NewItems(bus, events.BucketSearches, func(item schema.ItemRef, _ []byte) []reconcile.Request {
		if !strings.EqualFold(item.Kind, "Search") || item.Name == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: item.Namespace, Name: item.Name}}}
	})
}

// interactiveTask is an interactive Search's SearchTask v2 (§7.4): the
// Search's ref and seq key the answer; the item, its container, its
// profile, the enabled protocols, the indexer priorities and the release
// index scope are what the search agent no longer reads for itself. An
// item the cache does not hold (yet) leaves them empty: the agent reads
// the item by MediaRef and reports what it finds.
func (r *Reconciler) interactiveTask(ctx context.Context, s *catalogv1alpha1.Search) (schema.SearchTask, error) {
	ref := *s.Spec.MediaRef
	task := schema.SearchTask{
		MediaRef: ref, Keys: ref.Keys, Reason: schema.SearchReasonInteractive,
		SearchRef:   &schema.Ref{Namespace: s.Namespace, Name: s.Name, UID: string(s.UID)},
		UserInvoked: true, Seq: taskSeq(s),
	}
	if s.Spec.Season != nil {
		// An interactive season search (Wave S): the Series is the item,
		// with no container.
		n := *s.Spec.Season
		task.Season = &n
	}
	protocols, err := itemread.Protocols(ctx, r.Client, s.Namespace)
	if err != nil {
		return task, err
	}
	for p, on := range protocols {
		if on {
			task.Protocols = append(task.Protocols, p)
		}
	}
	slices.Sort(task.Protocols)
	idxs, err := itemread.Indexers(ctx, r.Client, s.Namespace)
	if err != nil {
		return task, err
	}
	task.IndexerPriorities = itemread.Priorities(idxs)
	item, owner, err := r.owner(ctx, s)
	if err != nil || item == nil {
		return task, err
	}
	task.Item = itemread.Ref(item)
	if cfg, err := itemread.ConfigOf(ctx, r.Client, item); err == nil {
		task.QualityProfile = cfg.QualityProfileRef
	}
	if owner != nil {
		oref := itemread.Ref(owner)
		task.Scope = schema.BlockScopeOf(oref)
		if oref.UID != task.Item.UID {
			task.Container = &oref
		}
	}
	return task, nil
}
