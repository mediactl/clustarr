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

package subtitlerequest

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
)

// The CRD defaults for SearchSpec and UpgradeSpec. The apiserver fills them
// on every object that reaches this controller, so these only matter for a
// duration an operator explicitly set to zero: "0s" is read as "use the
// default" rather than as "search on every reconcile", which would publish a
// fetch task per reconcile for every wanted language in the cluster.
const (
	defaultSearchInterval  = 6 * time.Hour
	defaultAdaptiveDelay   = 504 * time.Hour // 3w
	defaultAdaptiveDelta   = 168 * time.Hour // 1w
	defaultUpgradeInterval = 12 * time.Hour
)

// cadence is a SubtitleProfile's search and upgrade pacing, resolved to
// durations once per reconcile.
type cadence struct {
	// interval is the full-cadence delay between searches of one wanted
	// language (search.interval, 6h).
	interval time.Duration
	// delay is how long after the FIRST search a language keeps the full
	// cadence (search.adaptiveDelay, 3w) -- design spec §6.5's "initial+3w".
	delay time.Duration
	// delta is the reduced cadence once delay has elapsed
	// (search.adaptiveDelta, 1w) -- §6.5's "latest+1w".
	delta time.Duration

	upgradeEnabled  bool
	upgradeInterval time.Duration
	// lookback is upgrade.lookbackDays: a subtitle downloaded longer ago than
	// this is never upgraded again (research note §8: "the 7-day window is
	// the deadline").
	lookback time.Duration
	// minDelta is upgrade.minDeltaPoints, the "3" in §6.5's
	// "score < outOf−3". See doc.go, "The upgrade pass".
	minDelta int32
}

func orDefault(d metav1.Duration, def time.Duration) time.Duration {
	if d.Duration <= 0 {
		return def
	}
	return d.Duration
}

// cadenceFor resolves spec's search and upgrade blocks.
func cadenceFor(spec subtitlev1alpha1.SubtitleProfileSpec) cadence {
	return cadence{
		interval:        orDefault(spec.Search.Interval, defaultSearchInterval),
		delay:           orDefault(spec.Search.AdaptiveDelay, defaultAdaptiveDelay),
		delta:           orDefault(spec.Search.AdaptiveDelta, defaultAdaptiveDelta),
		upgradeEnabled:  spec.Upgrade.EnabledOrDefault(),
		upgradeInterval: orDefault(spec.Upgrade.Interval, defaultUpgradeInterval),
		lookback:        time.Duration(spec.Upgrade.LookbackDays) * 24 * time.Hour,
		minDelta:        spec.Upgrade.MinDeltaPoints,
	}
}

// gateOpen is Bazarr's is_search_active (research note §9, verbatim), the
// adaptive gate of design spec §6.5: a language never searched is always
// eligible; for adaptiveDelay after its first search it is eligible on every
// scheduled run (full cadence); after that only once adaptiveDelta has
// passed since its latest search.
func gateOpen(t time.Time, a commonv1alpha1.Attempts, c cadence) bool {
	if a.Initial == nil || a.Latest == nil {
		return true
	}
	if a.Initial.Add(c.delay).After(t) {
		return true
	}
	return !a.Latest.Add(c.delta).After(t)
}

// wantedDueAt is when a still-missing language is next searched: the full
// cadence after its latest search, pushed out to latest+delta when the
// adaptive gate is closed by then. A language never searched is due now.
//
// It is derived from attempts alone, never from the stored nextSearchAt, so
// a profile whose cadence changed reschedules every language on the next
// reconcile with no separate "profile changed" pass -- the stored
// nextSearchAt is this function's output, written back for kubectl.
func wantedDueAt(now time.Time, a commonv1alpha1.Attempts, c cadence) time.Time {
	if a.Latest == nil {
		return now
	}
	due := a.Latest.Add(c.interval)
	if !gateOpen(due, a, c) {
		due = a.Latest.Add(c.delta)
	}
	return due
}

// stamp records one dispatched search in a.
//
// Bazarr stamps failedAttempts only when a search returns nothing (research
// note §9); this stamps at dispatch instead, because the controller cannot
// see a search's outcome until the worker reports it and a success takes the
// language out of the wanted set anyway, so the two are equivalent for every
// language the gate is ever consulted about.
func stamp(a commonv1alpha1.Attempts, now time.Time) commonv1alpha1.Attempts {
	t := metav1.NewTime(now)
	if a.Initial == nil {
		a.Initial = &t
	}
	a.Latest = &t
	a.Count++
	return a
}

// upgradeCandidate reports whether a downloaded item qualifies for the
// upgrade pass (design spec §6.5, research note §8): upgrades enabled, the
// subtitle downloaded inside the lookback window, and a score that leaves
// room for a better candidate -- score < scoreOutOf − minDeltaPoints. An item
// with no scoreOutOf or no downloadedAt cannot be judged and is not a
// candidate: guessing either would publish upgrade searches forever.
//
// It reads score, scoreOutOf, state and downloadedAt, which are
// captionarr-worker's leaves. This controller never writes them.
func upgradeCandidate(now time.Time, it subtitlev1alpha1.SubtitleItem, c cadence) bool {
	if !c.upgradeEnabled {
		return false
	}
	if it.State != subtitlev1alpha1.SubtitleItemDownloaded && it.State != subtitlev1alpha1.SubtitleItemUpgradable {
		return false
	}
	if it.ScoreOutOf <= 0 || it.DownloadedAt == nil {
		return false
	}
	if it.Score >= it.ScoreOutOf-c.minDelta {
		return false
	}
	return now.Before(it.DownloadedAt.Add(c.lookback))
}

// upgradeCheckAt is when the upgrade pass next looks at an item that is not
// missing: one upgrade.interval after the later of its download and its
// latest search. It is the nextSearchAt of every satisfied item -- the
// liveness protocol requires one on every item the controller keeps (see
// doc.go) -- and a search follows it only when [upgradeScheduled] says so.
//
// An item with neither timestamp (created for a missing language that a
// file from elsewhere satisfied before any search ran) keeps fallback.
func upgradeCheckAt(it subtitlev1alpha1.SubtitleItem, c cadence, fallback time.Time) time.Time {
	var base time.Time
	if it.DownloadedAt != nil {
		base = it.DownloadedAt.Time
	}
	if a := it.Attempts.Latest; a != nil && a.After(base) {
		base = a.Time
	}
	if base.IsZero() {
		return fallback
	}
	return base.Add(c.upgradeInterval)
}

// upgradeScheduled reports whether the upgrade pass will really search an
// item at at: it qualifies now, and at still falls inside the lookback
// window. Past the window it is never upgraded again, and gets no wake-up.
func upgradeScheduled(now, at time.Time, it subtitlev1alpha1.SubtitleItem, c cadence) bool {
	return upgradeCandidate(now, it, c) && at.Before(it.DownloadedAt.Add(c.lookback))
}

// upgradeMinScore is the absolute minimum a replacement must score: §6.5's
// "minScore=score+1", but never below the profile's own threshold for a
// first download, so a profile whose minScorePercent was raised after the
// original download cannot be "upgraded" to a candidate it would reject
// outright.
func upgradeMinScore(score, threshold int32) int32 {
	return max(score+1, threshold)
}
