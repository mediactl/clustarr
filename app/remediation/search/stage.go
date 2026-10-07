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
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/controller/wantedcron"
	"github.com/mediactl/clustarr/app/catalog/history"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/dlindex"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;episodes;albums;books;audiobooks;comics;issues;artists;authors,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies/status;series/status;episodes/status;albums/status;books/status;audiobooks/status;issues/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=searches,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloadclients,verbs=get;list;watch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=get;list;watch

// Options is what the search stage reads.
type Options struct {
	Reader client.Reader
	Bus    events.Bus
	// Dispatch admits every automatic search; Book renders its delivery.
	Dispatch *dispatch.Ledger
	Book     *dispatch.DeliveryBook
	// Budget is the namespaces' first-time live donor budget, which
	// wantedcron refills.
	Budget *searchplan.DonorBudget
	// Clock is the stage's clock; nil is time.Now.
	Clock func() time.Time
}

// Stage is the search item stage (remediation.ItemStage, Watcher,
// SetDeclarer).
type Stage struct {
	o         Options
	searches  *records.Reader[*schema.SearchRecord]
	published *publishBook
}

var (
	_ remediation.ItemStage   = (*Stage)(nil)
	_ remediation.Watcher     = (*Stage)(nil)
	_ remediation.SetDeclarer = (*Stage)(nil)
)

// New builds the search stage.
func New(o Options) *Stage {
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return &Stage{
		o: o, searches: records.NewReader(o.Bus.KV(events.BucketSearches), agentrecords.Searches()),
		published: &publishBook{m: map[dispatch.Key]*searchplan.Task{}},
	}
}

// Name implements remediation.ItemStage.
func (s *Stage) Name() remediation.ItemStageName { return remediation.StageSearch }

// Applies implements remediation.ItemStage: the searchable kinds (R12),
// and a Series, whose person-requested series and season searches it
// plans (Wave S, planSeries).
func (s *Stage) Applies(k remediation.KeyKind) bool {
	return itemread.Searchable(string(k)) || k == remediation.KindSeries
}

// DeclaresSets implements remediation.SetDeclarer: searchDispatch,
// lastSearchedAt and the attempt counters, in catalog-grab's set -- on a
// Series, seasonSearch and lastSearchedAt beside the grab stage's
// pendingGrabs.
func (s *Stage) DeclaresSets(remediation.KeyKind) []k8s.FieldManager {
	return []k8s.FieldManager{k8s.ManagerCatalogGrab}
}

// Plan implements remediation.ItemStage.
func (s *Stage) Plan(ctx context.Context, env *remediation.Env, v *remediation.ItemView) (remediation.ItemResult, error) {
	if v.Key.Kind == remediation.KindSeries {
		return s.planSeries(ctx, env, v)
	}
	item := v.Item
	now := v.Now.Time
	sv, err := s.gather(ctx, item, v, now)
	if err != nil {
		return remediation.ItemResult{}, remediation.Transient(err)
	}
	if item.GetDeletionTimestamp() != nil {
		// A deleting item dispatches nothing; its bookkeeping is re-sent.
		sv.Searchable = false
	}
	admit := func(durable string, seq int64) dispatch.Decision {
		if s.o.Dispatch == nil {
			return dispatch.Decision{Admitted: true}
		}
		return s.o.Dispatch.Admit(durable, sv.Key, seq)
	}
	class := records.SearchClass(sv.Item.Namespace)
	pace := func() time.Time {
		if env == nil || env.Pacer == nil {
			return now
		}
		return env.Pacer.Reserve(class, sv.Item.UID)
	}
	plan := searchplan.Decide(sv, admit, pace)
	return s.result(sv, plan, class), nil
}

// result is the plan as the pass's outcome: catalog-grab's bookkeeping, the
// publish and the ledger's answer.
func (s *Stage) result(sv searchplan.View, plan searchplan.Plan, class string) remediation.ItemResult {
	d := plan.Dispatch
	if d != nil && d.InFlight() && s.o.Book != nil {
		// A nak or a dead letter the advisory intake recorded (R8).
		dispatch.Render(d, "", nil, s.o.Book, sv.Key)
	}
	fields := map[string]any{
		"searchDispatch": nilIf(d == nil, d),
		"lastSearchedAt": nilIf(plan.LastSearchedAt == nil, plan.LastSearchedAt),
		"searchAttempts": plan.Attempts,
	}
	if itemread.HasDonorAttempts(sv.Item.Kind) {
		fields["donorSearchAttempts"] = plan.DonorAttempts
	}
	res := remediation.ItemResult{
		Sets: []itempass.Set{{Manager: k8s.ManagerCatalogGrab, Fields: fields}},
		Due:  plan.Due,
	}
	if plan.Waiting != "" && s.o.Dispatch != nil {
		s.o.Dispatch.Waiting(searchplan.Durable, sv.Key, plan.Waiting)
	}
	if plan.Answered {
		res.Effects = append(res.Effects, remediation.DispatchAnswered{Durable: searchplan.Durable, Ledger: sv.Key, Seq: plan.AnsweredSeq})
	}
	if t := plan.Publish; t != nil {
		if t.FirstLiveDonor && !t.Republish {
			// The budget was checked before Decide; a concurrent pass may
			// have spent it since, which costs one search over it.
			s.o.Budget.Take(sv.Item.Namespace)
		}
		if eff, ok := publishTask(t, sv.Key, sv.Item, searchplan.Durable, class, sv.Item.UID); ok {
			res.Effects = append(res.Effects, eff)
			s.published.set(sv.Key, t)
		}
	}
	return res
}

// nilIf is v, or an untyped nil (absent in the set) when absent.
func nilIf(absent bool, v any) any {
	if absent {
		return nil
	}
	return v
}

// gather builds searchplan's view of one item from the cache, its record
// and the leader-local books (A4.2 step 2).
func (s *Stage) gather(ctx context.Context, item client.Object, v *remediation.ItemView, now time.Time) (searchplan.View, error) {
	r := s.o.Reader
	ref := itemread.Ref(item)
	sv := searchplan.View{
		Item: ref, Kind: itemread.MediaKind(item), Now: now,
		MediaRef: commonv1.MediaRef{Kind: itemread.MediaKind(item), Name: ref.Name},
		Dispatch: itemread.SearchDispatch(item), Searchable: true,
		Key: dispatch.Key{Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name, Sub: history.DispatchSubSearch},
	}
	cand, _ := wantedcron.CandidateOf(item, now)
	sv.Wanted, sv.Reason = cand.Reason != "", cand.Reason
	sv.Attempts, sv.DonorAttempts = cand.Attempts, cand.DonorAttempts
	sv.LastSearched = lastSearched(item)

	monitored, err := itemread.Monitored(ctx, r, item)
	if err != nil {
		return sv, err
	}
	sv.Monitored = monitored
	owner, err := itemread.Owner(ctx, r, item)
	if err != nil {
		return sv, err
	}
	if owner == nil {
		sv.Searchable = false // its container is gone: nothing could hold the grab
	} else {
		oref := itemread.Ref(owner)
		sv.Scope = schema.BlockScopeOf(oref)
		if oref.UID != ref.UID {
			sv.Container = &oref
		}
		if ep, ok := item.(*catalogv1alpha1.Episode); ok {
			// Its Series' season search (Wave S, §7): wait for a pending
			// season, fold an answered one.
			sv.SeasonPending, sv.SeasonAnsweredAt = seasonOf(owner, ep)
		}
	}
	cfg, err := itemread.ConfigOf(ctx, r, item)
	if err != nil {
		return sv, err
	}
	if _, err := itemread.Profile(ctx, r, cfg.QualityProfileRef); err == nil {
		sv.Profile = cfg.QualityProfileRef
	}
	protocols, err := itemread.Protocols(ctx, r, ref.Namespace)
	if err != nil {
		return sv, err
	}
	for p, on := range protocols {
		if on {
			sv.Protocols = append(sv.Protocols, p)
		}
	}
	slices.Sort(sv.Protocols)
	idxs, err := itemread.Indexers(ctx, r, ref.Namespace)
	if err != nil {
		return sv, err
	}
	sv.Priorities = itemread.Priorities(idxs)

	rec, _, ok, err := s.searches.Get(ctx, agentrecords.SearchKey(ref.UID))
	if err != nil {
		return sv, fmt.Errorf("search: record of %s: %w", ref.Name, err)
	}
	if ok {
		sv.Record = rec
	}
	entries := s.entries(owner, v)
	sv.Redownload = redownloadAt(entries, item, now)
	if cand.Donor && itemread.HasDonorAttempts(ref.Kind) {
		want, err := s.donorWant(ctx, item, entries)
		if err != nil {
			return sv, err
		}
		sv.Donor = want
		sv.DonorBudget = s.o.Budget.Room(ref.Namespace)
	}
	if d := sv.Dispatch; d.InFlight() {
		sv.InFlight = s.published.get(sv.Key, d.Seq)
	}
	return sv, nil
}

// entries is the owner's entries: this pass's downloads outcome when the
// owner is the item itself, else the stored ones.
func (s *Stage) entries(owner client.Object, v *remediation.ItemView) []catalogv1alpha1.DownloadEntry {
	if owner == nil {
		return nil
	}
	if v.Downloads != nil && owner.GetUID() == v.Item.GetUID() {
		return v.Downloads.Entries
	}
	return dlindex.Entries(owner)
}

// redownloadAt is R25's failure time: the newest Failed (a release fault,
// not a local fault or an expired hold) or Blocklisted entry covering item,
// younger than lifecycle.RedownloadWindow; nil for none.
func redownloadAt(entries []catalogv1alpha1.DownloadEntry, item client.Object, now time.Time) *time.Time {
	var at *time.Time
	for i := range entries {
		e := &entries[i]
		switch e.Phase {
		case commonv1.DownloadPhaseBlocklisted:
		case commonv1.DownloadPhaseFailed:
			if e.FailureReason == "" || e.FailureReason.IsLocalFault() || e.FailureReason == commonv1.DownloadFailureImportExpired {
				continue
			}
		default:
			continue
		}
		if !itemread.Covers(e, item) {
			continue
		}
		failed := e.Dispatch.DispatchedAt.UTC()
		if failed.IsZero() || now.Sub(failed) >= lifecycle.RedownloadWindow {
			continue
		}
		if at == nil || failed.After(*at) {
			f := failed
			at = &f
		}
	}
	return at
}

// donorWant is what an audio donor for item must carry (P4, anime
// dual-audio spec §6.1): the languages its status.audio says the file
// lacks, the original as the anchor, the donors a graft of it already failed
// with, and the donors in flight; nil when the original is unknown, so no
// donor could be aligned.
func (s *Stage) donorWant(ctx context.Context, item client.Object, entries []catalogv1alpha1.DownloadEntry) (*schema.DonorWant, error) {
	var audio *catalogv1alpha1.AudioState
	switch t := item.(type) {
	case *catalogv1alpha1.Movie:
		audio = t.Status.Audio
	case *catalogv1alpha1.Episode:
		audio = t.Status.Audio
	}
	anchor := itemread.OriginalLanguage(ctx, s.o.Reader, item)
	if audio == nil || len(audio.Missing) == 0 || anchor == "" {
		return nil, nil
	}
	// The donors a graft of the item already failed with are the item's own
	// (loop spec §2.14), in its status.audio.
	want := &schema.DonorWant{
		Languages: append([]string(nil), audio.Missing...), Anchor: anchor,
		Rejected: slices.Clone(audio.RejectedReleases),
	}
	for i := range entries {
		if e := &entries[i]; rollup.IsDonor(e) && rollup.EntryNonTerminal(e) && itemread.Covers(e, item) {
			want.InFlight = append(want.InFlight, e.Release.Title)
		}
	}
	return want, nil
}

// lastSearched is an item's status.lastSearchedAt.
func lastSearched(o client.Object) *metav1.Time {
	switch t := o.(type) {
	case *catalogv1alpha1.Movie:
		return t.Status.LastSearchedAt
	case *catalogv1alpha1.Episode:
		return t.Status.LastSearchedAt
	case *catalogv1alpha1.Album:
		return t.Status.LastSearchedAt
	case *catalogv1alpha1.Book:
		return t.Status.LastSearchedAt
	case *catalogv1alpha1.Audiobook:
		return t.Status.LastSearchedAt
	case *catalogv1alpha1.Issue:
		return t.Status.LastSearchedAt
	}
	return nil
}

// publishBook is the stage's leader-local record of the task it last
// published for each item's dispatch in flight: an unanswered task is
// republished from it under its Msg-Id; after a leader change it is empty,
// and searchplan rebuilds the task.
type publishBook struct {
	mu sync.Mutex
	m  map[dispatch.Key]*searchplan.Task
}

func (b *publishBook) get(k dispatch.Key, seq int64) *searchplan.Task {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t, ok := b.m[k]; ok && t.Payload.Seq == seq {
		cp := *t
		return &cp
	}
	return nil
}

func (b *publishBook) set(k dispatch.Key, t *searchplan.Task) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cp := *t
	cp.Republish = false
	b.m[k] = &cp
}
