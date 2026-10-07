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

package grab

import (
	"context"
	"errors"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/indexer/limits"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/app/remediation/dlindex"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// recordHorizon is how long after a dispatch an item's search record can
// still be grab input: the dispatch's own timeout (the search planner's
// 15 min) plus grabplan.CandidateWindow. A Series reads only the records of
// Episodes dispatched inside it.
const recordHorizon = 15*time.Minute + grabplan.CandidateWindow

// gathered is one owner's view and the covered items' objects, in
// v.Covered's order: the current files are read only when the plan or
// convergence needs them (fillCurrent).
type gathered struct {
	v       grabplan.View
	objects []client.Object
	filled  bool
}

// fillCurrent reads every covered item's current file, once.
func (s *Stage) fillCurrent(ctx context.Context, g *gathered) error {
	if g.filled {
		return nil
	}
	for i, o := range g.objects {
		cur, err := itemread.Current(ctx, s.o.Reader, o)
		if err != nil {
			return err
		}
		g.v.Covered[i].Current = cur
	}
	g.filled = true
	return nil
}

// gather builds grabplan's view of one owner from the cache, the records
// bucket, the inbox and the grab ring (A4.1 step 3). The covered items'
// current files are read only when there is something to decide.
func (s *Stage) gather(ctx context.Context, item client.Object, now time.Time) (*gathered, error) {
	g := &gathered{}
	if err := s.gatherInto(ctx, item, now, g); err != nil {
		return nil, err
	}
	if len(g.v.Candidates) > 0 || len(g.v.Pending) > 0 {
		if err := s.fillCurrent(ctx, g); err != nil {
			return nil, err
		}
	}
	return g, nil
}

func (s *Stage) gatherInto(ctx context.Context, item client.Object, now time.Time, g *gathered) error {
	r := s.o.Reader
	ns := item.GetNamespace()
	ref := itemread.Ref(item)
	g.v = grabplan.View{
		Owner: ref, Kind: itemread.MediaKind(item), Now: now,
		Entries: dlindex.Entries(item), Pending: pendingOf(item),
		EntryCap: catalogv1alpha1.MaxOwnerEntries, PendingCap: 1,
	}
	v := &g.v
	if v.Kind == commonv1.MediaKindSeries || v.Kind == commonv1.MediaKindComic {
		v.EntryCap, v.PendingCap = catalogv1alpha1.MaxContainerEntries, catalogv1alpha1.MaxContainerEntries
	}

	monitored, err := itemread.Monitored(ctx, r, item)
	if err != nil {
		return err
	}
	v.Monitored = monitored
	cfg, err := itemread.ConfigOf(ctx, r, item)
	if err != nil {
		return err
	}
	v.ProfileRef = cfg.QualityProfileRef
	switch p, err := itemread.Profile(ctx, r, cfg.QualityProfileRef); {
	case err == nil:
		v.Profile = p
	case errors.Is(err, itemread.ErrNoProfile):
		logging.FromContext(ctx).Debug("grab: the owner has no usable quality profile; its candidates are refused", "reason", err)
	default:
		return err
	}
	dspec, err := itemread.Delay(ctx, r, ns, cfg)
	if err != nil {
		return err
	}
	v.Delay = &dspec
	if v.Protocols, err = itemread.Protocols(ctx, r, ns); err != nil {
		return err
	}
	v.OriginalLanguageTag = itemread.OriginalLanguage(ctx, r, item)

	if err := s.covered(ctx, item, g); err != nil {
		return err
	}
	if err := s.candidates(ctx, item, g); err != nil {
		return err
	}
	if err := s.indexers(ctx, ns, v); err != nil {
		return err
	}
	return nil
}

// covered fills the items a candidate may cover -- the owner itself, or a
// Series' Episodes or a Comic's Issues -- each with its monitoring and its
// availability; their current files are fillCurrent's.
func (s *Stage) covered(ctx context.Context, item client.Object, g *gathered) error {
	r := s.o.Reader
	ns := item.GetNamespace()
	v := &g.v
	add := func(o client.Object, c grabplan.Covered) {
		v.Covered = append(v.Covered, c)
		g.objects = append(g.objects, o)
	}
	switch t := item.(type) {
	case *catalogv1alpha1.Series:
		var eps catalogv1alpha1.EpisodeList
		if err := r.List(ctx, &eps, client.InNamespace(ns), client.MatchingFields{series.EpisodeBySeriesRefIndex: t.Name}); err != nil {
			return fmt.Errorf("grab: list Episodes: %w", err)
		}
		for i := range eps.Items {
			ep := &eps.Items[i]
			c := coveredItem(ep, v.Now)
			c.Monitored = c.Monitored && v.Monitored
			c.Season, c.Number, c.Absolute = ep.Spec.SeasonNumber, ep.Spec.EpisodeNumber, ep.Status.AbsoluteNumber
			add(ep, c)
		}
	case *catalogv1alpha1.Comic:
		var issues catalogv1alpha1.IssueList
		if err := r.List(ctx, &issues, client.InNamespace(ns), client.MatchingFields{comic.IssueByComicRefIndex: t.Name}); err != nil {
			return fmt.Errorf("grab: list Issues: %w", err)
		}
		for i := range issues.Items {
			iss := &issues.Items[i]
			c := coveredItem(iss, v.Now)
			c.Monitored = c.Monitored && v.Monitored
			c.Issue = iss.Spec.Number
			add(iss, c)
		}
	default:
		add(item, coveredItem(item, v.Now))
	}
	return nil
}

// coveredItem is one item's own monitoring and availability (its
// container's monitoring is the caller's).
func coveredItem(item client.Object, now time.Time) grabplan.Covered {
	return grabplan.Covered{Ref: itemread.Ref(item), Monitored: itemread.OwnMonitored(item), Wanted: itemread.Available(item, now)}
}

// candidates gathers the owner's candidates: its parked intake candidates,
// and the search answers of the owner (or its covered Episodes or Issues)
// that answer each item's current searchDispatch inside the window (R7).
func (s *Stage) candidates(ctx context.Context, item client.Object, g *gathered) error {
	v := &g.v
	if s.o.Inbox != nil {
		for _, p := range s.o.Inbox.Take(intake.OwnerOf(v.Owner)) {
			v.Candidates = append(v.Candidates, grabplan.FromParked(p))
		}
	}
	container := v.Kind == commonv1.MediaKindSeries || v.Kind == commonv1.MediaKindComic
	for i, it := range g.objects {
		d := itemread.SearchDispatch(it)
		if d == nil || v.Now.Sub(d.DispatchedAt.Time) > recordHorizon {
			continue
		}
		rec, _, ok, err := s.searches.Get(ctx, agentrecords.SearchKey(string(it.GetUID())))
		if err != nil {
			return fmt.Errorf("grab: search record of %s: %w", it.GetName(), err)
		}
		if !ok || rec.Seq != d.Seq || v.Now.Sub(rec.FinishedAt) > grabplan.CandidateWindow {
			continue
		}
		by := commonv1.GrabSourceSearch
		if redownload(v.Entries, it, d.DispatchedAt.Time, v.Now) {
			by = commonv1.GrabSourceRedownload
		}
		var target *schema.ItemRef
		if container {
			t := v.Covered[i].Ref
			target = &t
		}
		cs := grabplan.FromRecord(rec, target, by)
		if rec.Purpose == schema.SearchPurposeAudioDonor && len(cs) > 0 {
			if err := s.donorRejected(ctx, it, v); err != nil {
				return err
			}
		}
		v.Candidates = append(v.Candidates, cs...)
	}
	return nil
}

// donorRejected fills the covered item's donors a graft already failed
// with, from its AudioGraft (until F7.1 folds it into the item).
func (s *Stage) donorRejected(ctx context.Context, it client.Object, v *grabplan.View) error {
	var g transcodev1alpha1.AudioGraft
	if err := s.o.Reader.Get(ctx, client.ObjectKey{Namespace: it.GetNamespace(), Name: k8s.AudioGraftName(it.GetName())}, &g); err != nil {
		return client.IgnoreNotFound(err)
	}
	for i := range v.Covered {
		if v.Covered[i].Ref.Name == it.GetName() {
			v.Covered[i].DonorRejected = g.Status.RejectedReleases
		}
	}
	return nil
}

// redownload reports a search answer that follows a failure (R25): a
// Failed (a release fault) or Blocklisted entry covering the item whose
// failure is older than the search dispatch and younger than
// lifecycle.RedownloadWindow. Its grab is recorded grabbedBy redownload.
func redownload(entries []catalogv1alpha1.DownloadEntry, item client.Object, dispatched, now time.Time) bool {
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
		failed := e.Dispatch.DispatchedAt.Time
		if !itemread.Covers(e, item) || failed.IsZero() || failed.After(dispatched) || now.Sub(failed) >= lifecycle.RedownloadWindow {
			continue
		}
		return true
	}
	return false
}

// indexers fills each candidate Indexer's seed criteria and, for one with a
// grab limit, when its window next has room (the ring read without
// reserving; P95's reservation is the index agent's).
func (s *Stage) indexers(ctx context.Context, ns string, v *grabplan.View) error {
	named := map[string]bool{}
	for i := range v.Candidates {
		if ref := v.Candidates[i].Release.Decision.IndexerRef; ref != "" {
			named[ref] = true
		}
	}
	for i := range v.Pending {
		if c := v.Pending[i].Candidate; c != nil && c.Release.IndexerRef != "" {
			named[c.Release.IndexerRef] = true
		}
	}
	if len(named) == 0 {
		return nil
	}
	idxs, err := itemread.Indexers(ctx, s.o.Reader, ns)
	if err != nil {
		return err
	}
	v.Seeds = map[string]*commonv1.SeedCriteria{}
	v.GrabLimited = map[string]time.Time{}
	kv := s.o.Bus.KV(events.BucketIndexerLimits)
	for i := range idxs {
		idx := &idxs[i]
		if !named[idx.Name] {
			continue
		}
		if idx.Spec.SeedCriteria != nil {
			v.Seeds[idx.Name] = idx.Spec.SeedCriteria.DeepCopy()
		}
		if limits.GrabLimit(idx) == nil {
			continue
		}
		u, err := limits.Grabs(ctx, kv, idx, v.Now)
		if err != nil {
			// A ring the manager cannot read fails open: the index agent's
			// payload fetch still refuses a grab past the limit (P95).
			logging.FromContext(ctx).Warn("grab: read an indexer's grab ring", "indexer", idx.Name, "error", err)
			continue
		}
		if u.RetryAt.After(v.Now) {
			v.GrabLimited[idx.Name] = u.RetryAt
		}
	}
	return nil
}

// pendingOf is an owner's stored pending grabs: pendingGrabs, or
// pendingGrab as a one-element list.
func pendingOf(o client.Object) []catalogv1alpha1.PendingGrab {
	one := func(pg *catalogv1alpha1.PendingGrab) []catalogv1alpha1.PendingGrab {
		if pg == nil {
			return nil
		}
		return []catalogv1alpha1.PendingGrab{*pg.DeepCopy()}
	}
	switch t := o.(type) {
	case *catalogv1alpha1.Movie:
		return one(t.Status.PendingGrab)
	case *catalogv1alpha1.Album:
		return one(t.Status.PendingGrab)
	case *catalogv1alpha1.Book:
		return one(t.Status.PendingGrab)
	case *catalogv1alpha1.Audiobook:
		return one(t.Status.PendingGrab)
	case *catalogv1alpha1.Series:
		return append([]catalogv1alpha1.PendingGrab(nil), t.Status.PendingGrabs...)
	case *catalogv1alpha1.Comic:
		return append([]catalogv1alpha1.PendingGrab(nil), t.Status.PendingGrabs...)
	}
	return nil
}
