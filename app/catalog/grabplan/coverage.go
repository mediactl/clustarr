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
	"github.com/mediactl/clustarr/pkg/quality"
)

// Coverage (Wave S, series search design §6.2-§6.3): which of a Series'
// Episodes a candidate covers, read from the search agent's scene-mapped
// coverage (schema.ParsedFacts.Coverage) when the record carries it, and
// how the covered items' re-checks fold into the release's verdict. A
// Comic's Issues are covered items too (2026-11-10 design D10: Episodes'
// pack rules, the volume being the one season), matched by the parsed
// issue number (narrow.go's issueFacts); every rule here that filters on a
// covered item's kind takes an Episode or an Issue (child).

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
	// upgrade it, or the entry is a pack (PackLive).
	Live bool
	// PackLive: a live pack covers the item, which it holds whatever the
	// release until it has imported or failed (owner, 2026-11-02).
	PackLive bool
	// Queued: a live video grab covers the item that outranks the release
	// by its audio (decision.AudioOutranked: under a profile taking complete
	// audio first, it carries every wanted language and the release does
	// not). A release covering several items is refused over it; over any
	// other live single a pack is grabbed and the single removed (owner,
	// 2026-11-03, which reversed 2026-11-02's "no pack while an episode it
	// covers is downloading").
	Queued bool
	// Unaired: the item has not aired.
	Unaired bool
}

// NoteQueue marks ic Live from one of the item's item-state rejections: a
// live grab the release does not upgrade (QueueHigherPreference), or a pack
// holding the item (PackDownloading), which is also PackLive.
func (ic *ItemCheck) NoteQueue(rej commonv1.Rejection) {
	switch {
	case decision.ReasonPackDownloading.Of(rej):
		ic.Live, ic.PackLive = true, true
	case decision.ReasonQueueHigherPreference.Of(rej):
		ic.Live = true
	}
}

// CoverageRejections folds the covered items into the release's verdict
// (series search design §6.3): nil takes it, narrowed to the items with no
// rejection; otherwise the rejections that refuse it -- PackDownloading when
// a live pack holds any item, EpisodeDownloading when the release covers
// several items and a live grab covers any of them, else
// QueueHigherPreference when any item is Live (all exempt with override:
// the payload would bring that episode a second time, Sonarr's
// QueueSpecification),
// FullSeasonNotAired for a full-season release with an Unaired item
// (exempt when manual: Sonarr's FullSeasonSpecification), else the first
// item's when every item is rejected (P23: a pack covering no item that
// wants it). The grab planner and the Search controller's interactive
// season search both decide through it, so a person sees exactly what the
// planner would decide. child is the covered items' kind, an Episode's or
// an Issue's, and words each refusal for it (design D10, §9.4): a volume
// search's release naming an issue the comic lacks reads "covers no issue
// of the comic" in the volume modal and at the grab check, never
// television's words.
func CoverageRejections(child commonv1.MediaKind, items []ItemCheck, season int32, fullSeason, manual, override bool) []commonv1.Rejection {
	n := nounsOf(child)
	if len(items) == 0 {
		return []commonv1.Rejection{coverageRejection(decision.ReasonWrongItem, "the release covers no "+n.child+" of the "+n.container)}
	}
	if !override {
		for _, it := range items {
			if it.PackLive {
				return []commonv1.Rejection{coverageRejection(decision.ReasonPackDownloading,
					"the release covers an "+n.child+" a pack still downloading holds; it is judged again once the pack has imported or failed")}
			}
		}
		if len(items) > 1 {
			for _, it := range items {
				if it.Queued {
					return []commonv1.Rejection{coverageRejection(decision.ReasonEpisodeDownloading,
						"the pack covers an "+n.child+" a grab carrying every wanted language is still downloading, and the pack does not carry them")}
				}
			}
		}
		for _, it := range items {
			if it.Live {
				return []commonv1.Rejection{coverageRejection(decision.ReasonQueueHigherPreference,
					"the release covers an "+n.child+" a grab of equal or higher preference is already downloading")}
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

// nouns are the words a coverage rejection names the covered items and
// their container by: an episode of a series, or an issue of a comic
// (design D10). Both children take "an".
type nouns struct {
	child, container string
	// notOut says the child is not out yet: an episode has not aired, an
	// issue has not been released.
	notOut string
}

var (
	episodeNouns = nouns{child: "episode", container: "series", notOut: "has not aired"}
	issueNouns   = nouns{child: "issue", container: "comic", notOut: "has not been released"}
)

// nounsOf are the nouns for covered items of kind child: an Issue's, else
// an Episode's -- the one other covered child; an owner covered by itself
// (a Movie, an Album) keeps the series wording, which reaches only the
// planner's verdict, whose reason is a guard (check) that nothing logs or
// shows.
func nounsOf(child commonv1.MediaKind) nouns {
	if child == commonv1.MediaKindIssue {
		return issueNouns
	}
	return episodeNouns
}

// coveredKind is a covered item's kind as a MediaKind ("Issue" is
// commonv1.MediaKindIssue): the child kind the planner words its coverage
// rejections by and judges its decision targets as.
func coveredKind(it Covered) commonv1.MediaKind {
	return commonv1.MediaKind(strings.ToLower(it.Ref.Kind))
}

// Rejections an automatic candidate's covered item adds, worded for its
// kind: not monitored (Sonarr's MonitoredEpisodeSpecification) and not out
// (aired, released).
func rejectionNotMonitored(n nouns) commonv1.Rejection {
	return commonv1.Rejection{Reason: "NotMonitored: the " + n.child + " is not monitored", Type: commonv1.RejectionPermanent}
}

func rejectionNotOut(n nouns) commonv1.Rejection {
	return coverageRejection(decision.ReasonUnavailable, "the "+n.child+" "+n.notOut)
}

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
		if !child(it.Ref.Kind) {
			continue
		}
		if full[it.Season] || pairs[[2]int32{it.Season, it.Number}] || (it.Absolute != nil && abs[*it.Absolute]) {
			add(it)
		}
	}
}

// child reports a container's covered child kind: an Episode of a Series or
// an Issue of a Comic (design D10), as against the owner itself.
func child(kind string) bool {
	return strings.EqualFold(kind, "Episode") || strings.EqualFold(kind, "Issue")
}

// seasonFacts are the covered items of season the parsed facts of a
// season answer name, for a record without coverage (an older agent): a
// full-season pack all of them, else its episode and absolute numbers.
func seasonFacts(p schema.ParsedFacts, season int32, covered []Covered, add func(Covered)) {
	if len(p.Seasons) > 0 && !containsInt32(p.Seasons, season) && len(p.Absolute) == 0 {
		return
	}
	for _, it := range covered {
		if it.Season != season || !child(it.Ref.Kind) {
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
// video grab covers it that the release does not upgrade or that is a pack,
// whether any live video grab covers it (Queued) -- under a profile taking
// complete audio first, one the release does not outrank by its audio
// (complete, decision.AudioOutranks) -- and whether it has aired.
func (d *decider) itemCheck(c Candidate, it Covered, complete bool) ItemCheck {
	ic := ItemCheck{Unaired: !it.Wanted}
	if !c.Manual {
		n := nounsOf(coveredKind(it))
		if !it.Monitored {
			ic.Rejections = append(ic.Rejections, rejectionNotMonitored(n))
		}
		if !it.Wanted {
			ic.Rejections = append(ic.Rejections, rejectionNotOut(n))
		}
	}
	ic.Queued = c.Purpose == "" && Holds(d.v.Profile, d.v.OriginalLanguageTag, d.queue(it, c.Purpose), complete)
	for _, rej := range d.itemStateRejections(c, it) {
		if c.Purpose == "" {
			ic.NoteQueue(rej)
		}
		if !exempt(c, rej) {
			ic.Rejections = append(ic.Rejections, rej)
		}
	}
	return ic
}

// Holds reports a queued grab that outranks a release (complete: carrying
// every language p's audio policy wants) by its audio
// (decision.AudioOutranked), which ItemCheck.Queued records: a pack is
// grabbed over every other single downloading, which it replaces (owner,
// 2026-11-03), never over a dual-audio single when it is not dual audio.
func Holds(p *quality.Profile, originalTag string, queue []decision.Queued, complete bool) bool {
	if p == nil {
		return false
	}
	for _, q := range queue {
		if decision.AudioOutranked(ctxBackground, *p, originalTag, complete, q) {
			return true
		}
	}
	return false
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
	case decision.ReasonQueueHigherPreference.Of(rejs[0]), decision.ReasonPackDownloading.Of(rejs[0]),
		decision.ReasonEpisodeDownloading.Of(rejs[0]):
		// Only a Live item yields it at the release's level: an item's own
		// queue rejection is Live's evidence, or exempt.
		return decisionOverlapsLive
	}
	return decisionRefused
}

// whole reports a candidate that is a whole season for pack preference
// (CompareEpisodeCount, rankscore.WholeSeason): a FullSeason release, or
// one whose coverage names every aired episode -- more than one -- of a
// season it covers (an anime batch); on a Comic, every released issue of
// the volume (design D10: a whole volume ranks as a whole season does).
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
			if it.Season == season && it.Wanted && child(it.Ref.Kind) {
				aired++
			}
		}
		if n > 1 && n == aired {
			return true
		}
	}
	return false
}
