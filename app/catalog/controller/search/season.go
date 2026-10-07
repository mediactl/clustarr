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
// reads rejected with the planner's reason: a pack overlapping a live grab
// QueueHigherPreference, a pack of a season still airing
// FullSeasonNotAired. A release covering every aired episode of the season
// is marked CoversSeason. A release for another title is left as the agent
// judged it. The result is banded and capped.
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

// seasonState is what seasonItemState reads of the Series once per answer.
type seasonState struct {
	ser      *catalogv1alpha1.Series
	episodes []*catalogv1alpha1.Episode
	profile  *quality.Profile
	language string
	reader   client.Reader
	now      time.Time
	// current caches each Episode's current file.
	current map[string]*decision.Current
}

// seasonState reads the Series, its Episodes and its resolved profile; ok
// is false when any is missing (no item-state rule runs).
func (r *Reconciler) seasonState(ctx context.Context, s *catalogv1alpha1.Search) (*seasonState, bool) {
	var ser catalogv1alpha1.Series
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Spec.MediaRef.Name}, &ser); err != nil {
		return nil, false
	}
	cfg, err := itemread.ConfigOf(ctx, r.Client, &ser)
	if err != nil {
		return nil, false
	}
	profile, err := itemread.Profile(ctx, r.Client, cfg.QualityProfileRef)
	if err != nil {
		return nil, false
	}
	var list catalogv1alpha1.EpisodeList
	if err := r.Client.List(ctx, &list, client.InNamespace(s.Namespace), client.MatchingFields{series.EpisodeBySeriesRefIndex: ser.Name}); err != nil {
		logging.FromContext(ctx).Warn("search: list a series' episodes", "series", ser.Name, "err", err)
		return nil, false
	}
	st := &seasonState{
		ser: &ser, profile: profile, reader: r.Client, now: r.now(),
		language: itemread.OriginalLanguage(ctx, r.Client, &ser),
		current:  map[string]*decision.Current{},
	}
	for i := range list.Items {
		st.episodes = append(st.episodes, &list.Items[i])
	}
	return st, true
}

// covered are the Episodes a result covers: every one its scene-mapped
// coverage names (any season), or, without it, the searched season's
// episodes its parsed facts name (a full-season pack all of them).
func (st *seasonState) covered(p schema.ParsedFacts, season int32) []*catalogv1alpha1.Episode {
	var out []*catalogv1alpha1.Episode
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

// check is one covered Episode's re-check, as the grab planner runs it for
// a person's pick: its item-state rejections, whether a live video grab
// covers it that the release does not upgrade, and whether it has aired.
func (st *seasonState) check(ctx context.Context, sr schema.ScoredRelease, ep *catalogv1alpha1.Episode) grabplan.ItemCheck {
	cur, ok := st.current[ep.Name]
	if !ok {
		c, err := itemread.Current(ctx, st.reader, ep)
		if err == nil {
			cur = c
		}
		st.current[ep.Name] = cur
	}
	entries := dlindex.Entries(st.ser)
	rel := sr.Decision.ReleaseInfo
	blocked := sr.Blocked != nil || itemread.Tombstoned(entries, ep, rel)
	t := decision.Target{
		Kind: commonv1.MediaKindEpisode, OriginalLanguageTag: st.language,
		Current: cur, Queue: itemread.Queue(entries, ep, ""),
		Blocklist: func(string, string) bool { return blocked },
	}
	ic := grabplan.ItemCheck{Unaired: !itemread.Available(ep, st.now)}
	rejs := decision.ItemStateRejections(ctx, t, *st.profile,
		decision.Decision{Release: rel, Score: int(sr.Score)}, decision.Options{UserInvoked: true})
	for _, rej := range rejs {
		if decision.ReasonQueueHigherPreference.Of(rej) {
			ic.Live = true
		}
	}
	ic.Rejections = rejs
	return ic
}

// coversSeason reports a release covering every aired Episode of season,
// at least one: a full-season pack or an anime batch, which the season
// view lists by default.
func (st *seasonState) coversSeason(eps []*catalogv1alpha1.Episode, season int32) bool {
	got := map[string]bool{}
	for _, ep := range eps {
		got[ep.Name] = true
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
