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

package grabplan

import (
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Coverage (Wave S, series search design §6.2-§6.3): which of a Series'
// Episodes a candidate covers, read from the search agent's scene-mapped
// coverage (schema.ParsedFacts.Coverage) when the record carries it, and
// how the covered items' re-checks fold into the release's verdict.

// The grab planner's decisions for the release-level refusals (the
// clustarr_search_decisions_total decision label under reason="grabplan").
const (
	decisionOverlapsLive    = "overlapsLive"
	decisionSeasonNotAired  = "seasonNotAired"
	decisionTooManyEpisodes = "tooManyEpisodes"
	decisionRefused         = "refused"
)

// ItemCheck is one covered item's re-check for CoverageRejections.
type ItemCheck struct {
	// Rejections are the item's item-state rejections a candidate is not
	// exempt from (and not monitored, not aired, for an automatic
	// candidate).
	Rejections []commonv1.Rejection
	// Live: a live video entry covers the item and the release does not
	// upgrade it.
	Live bool
	// Unaired: the item has not aired.
	Unaired bool
}

// CoverageRejections folds the covered items into the release's verdict
// (series search design §6.3): nil takes it, narrowed to the items with no
// rejection; otherwise the rejections that refuse it -- QueueHigherPreference
// when any item is Live (exempt with override: the payload would bring
// that episode a second time, Sonarr's QueueSpecification),
// FullSeasonNotAired for a full-season release with an Unaired item
// (exempt when manual: Sonarr's FullSeasonSpecification), else the first
// item's when every item is rejected (P23: a pack covering no item that
// wants it). The grab planner and the Search controller's interactive
// season search both decide through it, so a person sees exactly what the
// planner would decide.
func CoverageRejections(items []ItemCheck, season int32, fullSeason, manual, override bool) []commonv1.Rejection {
	if len(items) == 0 {
		return []commonv1.Rejection{coverageRejection(decision.ReasonWrongItem, "the release covers no episode of the series")}
	}
	if !override {
		for _, it := range items {
			if it.Live {
				return []commonv1.Rejection{coverageRejection(decision.ReasonQueueHigherPreference,
					"the release covers an episode a grab of equal or higher preference is already downloading")}
			}
		}
	}
	if fullSeason && !manual {
		for _, it := range items {
			if it.Unaired {
				return []commonv1.Rejection{decision.FullSeasonNotAired(int(season))}
			}
		}
	}
	for _, it := range items {
		if len(it.Rejections) == 0 {
			return nil
		}
	}
	return append([]commonv1.Rejection(nil), items[0].Rejections...)
}

// coverageRejection renders a pkg/decision reason the way pkg/decision
// does ("<Code>: <detail>"), so Reason.Of reads it.
func coverageRejection(r decision.Reason, detail string) commonv1.Rejection {
	return commonv1.Rejection{Reason: r.Code + ": " + detail, Type: r.Type}
}

// Rejections an automatic candidate's covered item adds: not monitored
// (Sonarr's MonitoredEpisodeSpecification) and not aired.
var (
	rejectionNotMonitored = commonv1.Rejection{Reason: "NotMonitored: the episode is not monitored", Type: commonv1.RejectionPermanent}
	rejectionNotAired     = coverageRejection(decision.ReasonUnavailable, "the episode has not aired")
)

// FromSeasonRecord turns a season answer into candidates (series search
// design §6.2): each approved release, GrabbedBy, Season set, NoDelay (a
// person asked, and a user-invoked search passes the delay profile, as
// Sonarr's does). The caller checks the record answers the season's seq.
func FromSeasonRecord(rec *schema.SearchRecord, season int32, grabbedBy commonv1.GrabSource) []Candidate {
	cs := FromRecord(rec, nil, grabbedBy)
	for i := range cs {
		n := season
		cs[i].Season = &n
		cs[i].NoDelay = true
	}
	return cs
}

// coveredBy are the Series' covered items rec names: every season it is a
// whole pack of, every season and episode pair and every absolute number.
func coveredBy(cov *schema.ReleaseCoverage, covered []Covered, add func(Covered)) {
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
	for _, it := range covered {
		if !strings.EqualFold(it.Ref.Kind, "Episode") {
			continue
		}
		if full[it.Season] || pairs[[2]int32{it.Season, it.Number}] || (it.Absolute != nil && abs[*it.Absolute]) {
			add(it)
		}
	}
}

// seasonFacts are the covered items of season the parsed facts of a
// season answer name, for a record without coverage (an older agent): a
// full-season pack all of them, else its episode and absolute numbers.
func seasonFacts(p schema.ParsedFacts, season int32, covered []Covered, add func(Covered)) {
	if len(p.Seasons) > 0 && !containsInt32(p.Seasons, season) && len(p.Absolute) == 0 {
		return
	}
	for _, it := range covered {
		if it.Season != season || !strings.EqualFold(it.Ref.Kind, "Episode") {
			continue
		}
		switch {
		case p.FullSeason:
			add(it)
		case containsInt32(p.Episodes, it.Number):
			add(it)
		case it.Absolute != nil && containsInt32(p.Absolute, *it.Absolute):
			add(it)
		}
	}
}

// itemCheck is one covered item's re-check (series search design §6.3):
// the item-state rejections the candidate is not exempt from -- plus not
// monitored and not aired for an automatic candidate -- whether a live
// video grab covers it that the release does not upgrade, and whether it
// has aired.
func (d *decider) itemCheck(c Candidate, it Covered) ItemCheck {
	ic := ItemCheck{Unaired: !it.Wanted}
	if !c.Manual {
		if !it.Monitored {
			ic.Rejections = append(ic.Rejections, rejectionNotMonitored)
		}
		if !it.Wanted {
			ic.Rejections = append(ic.Rejections, rejectionNotAired)
		}
	}
	for _, rej := range d.itemStateRejections(c, it) {
		if c.Purpose == "" && decision.ReasonQueueHigherPreference.Of(rej) {
			ic.Live = true
		}
		if !exempt(c, rej) {
			ic.Rejections = append(ic.Rejections, rej)
		}
	}
	return ic
}

// refusalDecision is the metric decision of a release CoverageRejections
// refused.
func refusalDecision(rejs []commonv1.Rejection) string {
	if len(rejs) == 0 {
		return decisionRefused
	}
	switch {
	case decision.ReasonFullSeasonNotAired.Of(rejs[0]):
		return decisionSeasonNotAired
	case decision.ReasonQueueHigherPreference.Of(rejs[0]):
		// Only a Live item yields it at the release's level: an item's own
		// queue rejection is Live's evidence, or exempt.
		return decisionOverlapsLive
	}
	return decisionRefused
}

// whole reports a candidate that is a whole season for pack preference
// (CompareEpisodeCount): a FullSeason release, or one whose coverage names
// every aired episode -- more than one -- of a season it covers (an anime
// batch).
func (d *decider) whole(vd verdict) bool {
	if vd.c.Release.Parsed.FullSeason {
		return true
	}
	if len(vd.covered) == 0 {
		return false
	}
	got := map[int32]int{}
	for _, it := range vd.covered {
		if it.Wanted {
			got[it.Season]++
		}
	}
	for season, n := range got {
		aired := 0
		for _, it := range d.v.Covered {
			if it.Season == season && it.Wanted && strings.EqualFold(it.Ref.Kind, "Episode") {
				aired++
			}
		}
		if n > 1 && n == aired {
			return true
		}
	}
	return false
}
