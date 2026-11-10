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
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/app/remediation/dlindex"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality"
)

// seasonItemState is withItemState for an interactive season search
// (series search design §8.1): for each result it resolves the Episodes
// the release covers -- its scene-mapped coverage, or without one the
// searched season's episodes its parsed facts name -- runs pkg/decision's
// item-state rules per Episode (its current file, the Series' entries as
// its queue, the blocklist), and folds them with
// grabplan.CoverageRejections, so a release the grab planner would refuse
// reads rejected with the planner's reason: a release overlapping a live
// pack PackDownloading, a pack overlapping any live grab
// EpisodeDownloading, a pack of a season still airing
// FullSeasonNotAired. A release covering every aired episode of the season
// is marked CoversSeason. A release for another title is left as the agent
// judged it. The result is banded and capped. A comic's interactive volume
// search (2026-11-10 design D10) is judged the same way over the Comic and
// its Issues: each release covers the Issue its parsed issue number names,
// and one naming no issue of the comic is refused as the grab planner
// refuses it.
func (r *Reconciler) seasonItemState(ctx context.Context, s *catalogv1alpha1.Search, cands []schema.ScoredRelease) ([]commonv1.ReleaseDecision, int32) {
	season := *s.Spec.Season
	st, ok := r.seasonState(ctx, s)
	out := make([]commonv1.ReleaseDecision, 0, len(cands))
	for _, sr := range cands {
		rd := *sr.Decision.DeepCopy()
		if !ok || decision.BandOf(rd.Approved, rd.Rejections) == decision.BandOtherTitle {
			out = append(out, rd)
			continue
		}
		eps := st.covered(sr.Parsed, season)
		checks := make([]grabplan.ItemCheck, 0, len(eps))
		for _, ep := range eps {
			checks = append(checks, st.check(ctx, sr, ep))
		}
		if rejs := grabplan.CoverageRejections(checks, season, sr.Parsed.FullSeason, false, false); rejs != nil {
			rd.Rejections = append(rd.Rejections, rejs...)
			if len(rd.Rejections) > maxRejections {
				rd.Rejections = rd.Rejections[:maxRejections]
			}
			rd.Approved = false
			rd.TemporarilyRejected = allTemporary(rd.Rejections)
		}
		rd.CoversSeason = st.coversSeason(eps, season)
		out = append(out, rd)
	}
	return banded(out, keepLimit(s))
}

// seasonState is what seasonItemState reads of the container once per
// answer: a Series and its Episodes, or a Comic and its Issues (2026-11-10
// design D10: the one season being the volume).
type seasonState struct {
	// container is the grab owner: the Series or the Comic.
	container client.Object
	// kind is the covered items' kind, episode or issue.
	kind     commonv1.MediaKind
	episodes []*catalogv1alpha1.Episode
	issues   []*catalogv1alpha1.Issue
	profile  *quality.Profile
	language string
	reader   client.Reader
	now      time.Time
	// current caches each item's current file.
	current map[string]*decision.Current
}

// seasonState reads the container, its children and its resolved profile:
// a Series and its Episodes (series.EpisodeBySeriesRefIndex), or a Comic
// and its Issues (comic.IssueByComicRefIndex); ok is false when any is
// missing (no item-state rule runs).
func (r *Reconciler) seasonState(ctx context.Context, s *catalogv1alpha1.Search) (*seasonState, bool) {
	container, ok := itemread.New(string(s.Spec.MediaRef.Kind))
	if !ok {
		return nil, false
	}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Spec.MediaRef.Name}, container); err != nil {
		return nil, false
	}
	cfg, err := itemread.ConfigOf(ctx, r.Client, container)
	if err != nil {
		return nil, false
	}
	profile, err := itemread.ItemProfile(ctx, r.Client, container, cfg.QualityProfileRef)
	if err != nil {
		return nil, false
	}
	st := &seasonState{
		container: container, profile: profile, reader: r.Client, now: r.now(),
		language: itemread.OriginalLanguage(ctx, r.Client, container),
		current:  map[string]*decision.Current{},
	}
	switch c := container.(type) {
	case *catalogv1alpha1.Series:
		st.kind = commonv1.MediaKindEpisode
		var list catalogv1alpha1.EpisodeList
		if err := r.Client.List(ctx, &list, client.InNamespace(s.Namespace), client.MatchingFields{series.EpisodeBySeriesRefIndex: c.Name}); err != nil {
			logging.FromContext(ctx).Warn("search: list a series' episodes", "series", c.Name, "err", err)
			return nil, false
		}
		for i := range list.Items {
			st.episodes = append(st.episodes, &list.Items[i])
		}
	case *catalogv1alpha1.Comic:
		st.kind = commonv1.MediaKindIssue
		var list catalogv1alpha1.IssueList
		if err := r.Client.List(ctx, &list, client.InNamespace(s.Namespace), client.MatchingFields{comic.IssueByComicRefIndex: c.Name}); err != nil {
			logging.FromContext(ctx).Warn("search: list a comic's issues", "comic", c.Name, "err", err)
			return nil, false
		}
		for i := range list.Items {
			st.issues = append(st.issues, &list.Items[i])
		}
	default:
		return nil, false
	}
	return st, true
}

// covered are the items a result covers. On a Series, the Episodes: every
// one its scene-mapped coverage names (any season), or, without it, the
// searched season's episodes its parsed facts name (a full-season pack all
// of them). On a Comic, the Issue the release's parsed issue number names
// (decision.IssueNumberKey, so "001" and "1" are one issue), as the grab
// planner attributes a volume answer's releases (grabplan's issueFacts); a
// release naming no issue covers none.
func (st *seasonState) covered(p schema.ParsedFacts, season int32) []client.Object {
	if st.kind == commonv1.MediaKindIssue {
		return st.coveredIssues(p)
	}
	var out []client.Object
	if cov := p.Coverage; cov != nil {
		full := map[int32]bool{}
		for _, s := range cov.FullSeasons {
			full[s] = true
		}
		pairs := map[[2]int32]bool{}
		for _, e := range cov.Episodes {
			pairs[[2]int32{e.Season, e.Episode}] = true
		}
		abs := map[int32]bool{}
		for _, a := range cov.Absolute {
			abs[a] = true
		}
		for _, ep := range st.episodes {
			a := ep.Status.AbsoluteNumber
			if full[ep.Spec.SeasonNumber] || pairs[[2]int32{ep.Spec.SeasonNumber, ep.Spec.EpisodeNumber}] || (a != nil && abs[*a]) {
				out = append(out, ep)
			}
		}
		return out
	}
	if len(p.Seasons) > 0 && !containsInt32(p.Seasons, season) && len(p.Absolute) == 0 {
		return nil
	}
	for _, ep := range st.episodes {
		if ep.Spec.SeasonNumber != season {
			continue
		}
		a := ep.Status.AbsoluteNumber
		if p.FullSeason || containsInt32(p.Episodes, ep.Spec.EpisodeNumber) || (a != nil && containsInt32(p.Absolute, *a)) {
			out = append(out, ep)
		}
	}
	return out
}

// coveredIssues is covered on a Comic: the Issues whose number is the
// release's.
func (st *seasonState) coveredIssues(p schema.ParsedFacts) []client.Object {
	want := decision.IssueNumberKey(p.Issue)
	if want == "" {
		return nil
	}
	var out []client.Object
	for _, iss := range st.issues {
		if iss.Spec.Number != "" && decision.IssueNumberKey(iss.Spec.Number) == want {
			out = append(out, iss)
		}
	}
	return out
}

// check is one covered item's re-check, as the grab planner runs it for a
// person's pick: its item-state rejections, whether a live video grab
// covers it that the release does not upgrade, and whether it has aired
// (an Issue: come out).
func (st *seasonState) check(ctx context.Context, sr schema.ScoredRelease, item client.Object) grabplan.ItemCheck {
	cur, ok := st.current[item.GetName()]
	if !ok {
		c, err := itemread.Current(ctx, st.reader, item)
		if err == nil {
			cur = c
		}
		st.current[item.GetName()] = cur
	}
	entries := dlindex.Entries(st.container)
	rel := sr.Decision.ReleaseInfo
	blocked := sr.Blocked != nil || itemread.Tombstoned(entries, item, rel)
	t := decision.Target{
		Kind: st.kind, OriginalLanguageTag: st.language,
		Current: cur, Queue: itemread.Queue(entries, item, ""),
		Blocklist: func(string, string) bool { return blocked },
	}
	// Under completeFirst a dual-audio release outranks Japanese-only grabs
	// downloading, so they do not hold a pack (grabplan.Holds).
	complete := false
	if st.profile.AudioCompleteFirst {
		if parsed, err := decision.ParseForItemState(ctx, st.kind, st.language, rel); err == nil {
			complete = decision.ReleaseCompletesAudio(ctx, *st.profile, st.language, parsed)
		}
	}
	ic := grabplan.ItemCheck{Unaired: !itemread.Available(item, st.now), Queued: grabplan.Holds(st.profile, st.language, t.Queue, complete)}
	rejs := decision.ItemStateRejections(ctx, t, *st.profile,
		decision.Decision{Release: rel, Score: int(sr.Score)}, decision.Options{UserInvoked: true})
	for _, rej := range rejs {
		ic.NoteQueue(rej)
	}
	ic.Rejections = rejs
	return ic
}

// coversSeason reports a release covering every aired Episode of season,
// at least one: a full-season pack or an anime batch, which the season
// view lists by default. A comic release covers one Issue (covered), so a
// volume search's release is never a pack: the volume modal lists every
// release.
func (st *seasonState) coversSeason(items []client.Object, season int32) bool {
	if st.kind == commonv1.MediaKindIssue {
		return false
	}
	got := map[string]bool{}
	for _, it := range items {
		got[it.GetName()] = true
	}
	aired := 0
	for _, ep := range st.episodes {
		if ep.Spec.SeasonNumber != season || !itemread.Available(ep, st.now) {
			continue
		}
		aired++
		if !got[ep.Name] {
			return false
		}
	}
	return aired > 0
}

// banded orders results approved, rejected, then other titles
// (decision.BandOf, series search design §8.2), each band in the agent's
// rank order; an item-state rejection moves a release from approved to
// rejected, never into other titles. It caps at limit -- other titles go
// first -- stamps Rank as the 1-based position, and returns how many of
// the kept results, at their end, are other titles.
func banded(results []commonv1.ReleaseDecision, limit int) ([]commonv1.ReleaseDecision, int32) {
	bands := [3][]commonv1.ReleaseDecision{}
	for _, rd := range results {
		b := decision.BandOf(rd.Approved, rd.Rejections)
		bands[b] = append(bands[b], rd)
	}
	out := make([]commonv1.ReleaseDecision, 0, len(results))
	out = append(out, bands[decision.BandApproved]...)
	out = append(out, bands[decision.BandRejected]...)
	out = append(out, bands[decision.BandOtherTitle]...)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	var other int32
	for i := range out {
		out[i].Rank = int32(i + 1) //nolint:gosec // capped at MaxResults
		if decision.BandOf(out[i].Approved, out[i].Rejections) == decision.BandOtherTitle {
			other++
		}
	}
	return out, other
}

func containsInt32(s []int32, v int32) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
