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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
)

var t0 = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) *metav1.Time { t := metav1.NewTime(t0.Add(d)); return &t }

const day = 24 * time.Hour

// defaultCadence is the CRD's defaults, as the apiserver fills them.
var defaultCadence = cadenceFor(subtitlev1alpha1.SubtitleProfileSpec{
	Upgrade: subtitlev1alpha1.UpgradeSpec{Enabled: ptr.To(true), LookbackDays: 7, MinDeltaPoints: 3},
})

func TestCadenceDefaultsAZeroDuration(t *testing.T) {
	c := defaultCadence
	assert.Equal(t, 6*time.Hour, c.interval)
	assert.Equal(t, 21*day, c.delay, "§6.5's initial+3w")
	assert.Equal(t, 7*day, c.delta, "§6.5's latest+1w")
	assert.Equal(t, 12*time.Hour, c.upgradeInterval, "§6.5's 12h upgrade pass")
	assert.Equal(t, 7*day, c.lookback)
	assert.EqualValues(t, 3, c.minDelta, "§6.5's outOf−3")
}

// TestAdaptiveGate is research note §9's is_search_active, case by case.
func TestAdaptiveGate(t *testing.T) {
	tests := []struct {
		name string
		a    commonv1alpha1.Attempts
		now  time.Duration
		open bool
	}{
		{"never searched", commonv1alpha1.Attempts{}, 0, true},
		{"inside the full-cadence window", commonv1alpha1.Attempts{Initial: at(0), Latest: at(20 * day)}, 20*day + time.Hour, true},
		{"window closed, latest+delta not reached", commonv1alpha1.Attempts{Initial: at(0), Latest: at(20 * day)}, 22 * day, false},
		{"window closed, latest+delta reached exactly", commonv1alpha1.Attempts{Initial: at(0), Latest: at(20 * day)}, 27 * day, true},
		{"initial+delay is not > now at the boundary", commonv1alpha1.Attempts{Initial: at(0), Latest: at(20 * day)}, 21 * day, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.open, gateOpen(t0.Add(tc.now), tc.a, defaultCadence))
		})
	}
}

func TestWantedDueAt(t *testing.T) {
	now := t0.Add(30 * day)
	assert.Equal(t, now, wantedDueAt(now, commonv1alpha1.Attempts{}, defaultCadence), "a language never searched is due now")

	fresh := commonv1alpha1.Attempts{Initial: at(0), Latest: at(time.Hour), Count: 2}
	assert.Equal(t, t0.Add(7*time.Hour), wantedDueAt(now, fresh, defaultCadence), "full cadence: latest + interval")

	// Latest at 20d23h: latest+6h is 21d05h, past initial+3w, so the gate is
	// closed then and the next search is latest+1w.
	aging := commonv1alpha1.Attempts{Initial: at(0), Latest: at(20*day + 23*time.Hour)}
	assert.Equal(t, t0.Add(27*day+23*time.Hour), wantedDueAt(now, aging, defaultCadence))
}

func TestStamp(t *testing.T) {
	a := stamp(commonv1alpha1.Attempts{}, t0)
	assert.Equal(t, t0, a.Initial.Time)
	assert.Equal(t, t0, a.Latest.Time)
	assert.EqualValues(t, 1, a.Count)

	a = stamp(a, t0.Add(time.Hour))
	assert.Equal(t, t0, a.Initial.Time, "initial is the FIRST search and never moves")
	assert.Equal(t, t0.Add(time.Hour), a.Latest.Time)
	assert.EqualValues(t, 2, a.Count)
}

func TestUpgradeCandidate(t *testing.T) {
	now := t0.Add(2 * day)
	base := subtitlev1alpha1.SubtitleItem{
		LangKey: "en", State: subtitlev1alpha1.SubtitleItemDownloaded,
		Score: 170, ScoreOutOf: 180, DownloadedAt: at(day),
	}
	with := func(mut func(*subtitlev1alpha1.SubtitleItem)) subtitlev1alpha1.SubtitleItem {
		it := base
		mut(&it)
		return it
	}
	tests := []struct {
		name string
		it   subtitlev1alpha1.SubtitleItem
		c    cadence
		want bool
	}{
		{"score well below outOf−3", base, defaultCadence, true},
		{"score == outOf−4", with(func(it *subtitlev1alpha1.SubtitleItem) { it.Score = 176 }), defaultCadence, true},
		{"score == outOf−3 is not < outOf−3", with(func(it *subtitlev1alpha1.SubtitleItem) { it.Score = 177 }), defaultCadence, false},
		{"upgradable state qualifies", with(func(it *subtitlev1alpha1.SubtitleItem) { it.State = subtitlev1alpha1.SubtitleItemUpgradable }), defaultCadence, true},
		{"unavailable does not", with(func(it *subtitlev1alpha1.SubtitleItem) { it.State = subtitlev1alpha1.SubtitleItemUnavailable }), defaultCadence, false},
		{"no scoreOutOf cannot be judged", with(func(it *subtitlev1alpha1.SubtitleItem) { it.ScoreOutOf = 0 }), defaultCadence, false},
		{"no downloadedAt cannot be judged", with(func(it *subtitlev1alpha1.SubtitleItem) { it.DownloadedAt = nil }), defaultCadence, false},
		{"outside the lookback window", with(func(it *subtitlev1alpha1.SubtitleItem) { it.DownloadedAt = at(-6 * day) }), defaultCadence, false},
		{"upgrades disabled", base, func() cadence { c := defaultCadence; c.upgradeEnabled = false; return c }(), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, upgradeCandidate(now, tc.it, tc.c))
		})
	}
}

func TestUpgradeCheckAt(t *testing.T) {
	fallback := t0.Add(99 * time.Hour)
	it := subtitlev1alpha1.SubtitleItem{DownloadedAt: at(0)}
	assert.Equal(t, t0.Add(12*time.Hour), upgradeCheckAt(it, defaultCadence, fallback),
		"the first check is one interval after the download")

	it.Attempts = commonv1alpha1.Attempts{Latest: at(-time.Hour)}
	assert.Equal(t, t0.Add(12*time.Hour), upgradeCheckAt(it, defaultCadence, fallback),
		"a search from before the download does not count")

	it.Attempts = commonv1alpha1.Attempts{Latest: at(3 * day)}
	assert.Equal(t, t0.Add(3*day+12*time.Hour), upgradeCheckAt(it, defaultCadence, fallback))

	assert.Equal(t, fallback, upgradeCheckAt(subtitlev1alpha1.SubtitleItem{}, defaultCadence, fallback),
		"an item neither downloaded nor searched keeps the fallback")
}

func TestUpgradeScheduled(t *testing.T) {
	now := t0.Add(day)
	it := subtitlev1alpha1.SubtitleItem{
		State: subtitlev1alpha1.SubtitleItemDownloaded, Score: 100, ScoreOutOf: 180, DownloadedAt: at(0),
	}
	assert.True(t, upgradeScheduled(now, t0.Add(6*day), it, defaultCadence))
	assert.False(t, upgradeScheduled(now, t0.Add(7*day), it, defaultCadence),
		"a check at or past the lookback window never searches")
	it.Score = 178
	assert.False(t, upgradeScheduled(now, t0.Add(2*day), it, defaultCadence), "not a candidate")
}

func TestUpgradeMinScore(t *testing.T) {
	assert.EqualValues(t, 151, upgradeMinScore(150, 126), "§6.5: minScore = score+1")
	assert.EqualValues(t, 126, upgradeMinScore(100, 126), "never below the first-download threshold")
}

func TestRequeueAfter(t *testing.T) {
	assert.Zero(t, requeueAfter(t0, time.Time{}), "nothing scheduled: the watches bring it back")
	assert.Equal(t, 6*time.Hour, requeueAfter(t0, t0.Add(6*time.Hour)))
	assert.Equal(t, minRequeue, requeueAfter(t0, t0.Add(-time.Hour)), "an overdue wake is floored, never a hot loop")
}

func TestSelectProfile(t *testing.T) {
	mk := func(name string, def bool, sel *metav1.LabelSelector, created time.Duration, invalid bool) subtitlev1alpha1.SubtitleProfile {
		p := subtitlev1alpha1.SubtitleProfile{
			ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(t0.Add(created))},
			Spec:       subtitlev1alpha1.SubtitleProfileSpec{Default: def, Selector: sel},
		}
		if invalid {
			p.Status.Conditions = []metav1.Condition{{Type: subtitlev1alpha1.SubtitleProfileConditionInvalid, Status: metav1.ConditionTrue}}
		}
		return p
	}
	anime := &metav1.LabelSelector{MatchLabels: map[string]string{"genre": "anime"}}
	labelsAnime := labels.Set{"genre": "anime"}

	name := func(p *subtitlev1alpha1.SubtitleProfile) string {
		if p == nil {
			return ""
		}
		return p.Name
	}

	p, err := selectProfile([]subtitlev1alpha1.SubtitleProfile{
		mk("default", true, nil, 0, false), mk("z-anime", false, anime, 0, false), mk("a-anime", false, anime, 0, false),
	}, labelsAnime)
	assert.NoError(t, err)
	assert.Equal(t, "a-anime", name(p), "a matching selector beats the default; ties break by name")

	p, _ = selectProfile([]subtitlev1alpha1.SubtitleProfile{
		mk("default", true, nil, 0, false), mk("anime", false, anime, 0, true),
	}, labelsAnime)
	assert.Equal(t, "default", name(p), "an Invalid profile is never selected")

	p, _ = selectProfile([]subtitlev1alpha1.SubtitleProfile{
		mk("newer", true, nil, time.Hour, false), mk("older", true, nil, 0, false),
	}, nil)
	assert.Equal(t, "older", name(p), "the oldest valid default wins")

	p, _ = selectProfile([]subtitlev1alpha1.SubtitleProfile{mk("anime", false, anime, 0, false)}, nil)
	assert.Nil(t, p, "nothing selects and no default: no profile")

	_, err = selectProfile([]subtitlev1alpha1.SubtitleProfile{mk("bad", false, &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "k", Operator: "Bogus"}},
	}, 0, false)}, nil)
	assert.Error(t, err, "a broken selector is reported, not skipped in favour of the default")
}

// TestWorkerSignal pins the For() predicate's projection: it moves on every
// captionarr-worker leaf and on none of this controller's own, so a worker's
// report wakes the controller and the controller's own apply does not.
func TestWorkerSignal(t *testing.T) {
	base := &subtitlev1alpha1.SubtitleRequest{Status: subtitlev1alpha1.SubtitleRequestStatus{
		Phase: subtitlev1alpha1.SubtitleRequestPhaseWanted,
		Items: []subtitlev1alpha1.SubtitleItem{
			{LangKey: "fr", State: subtitlev1alpha1.SubtitleItemUnavailable},
			{LangKey: "en", State: subtitlev1alpha1.SubtitleItemDownloaded, Score: 100, ScoreOutOf: 180, DownloadedAt: at(0)},
		},
	}}
	sig := workerSignal(base)

	worker := map[string]func(*subtitlev1alpha1.SubtitleItem){
		"state":        func(it *subtitlev1alpha1.SubtitleItem) { it.State = subtitlev1alpha1.SubtitleItemSearching },
		"score":        func(it *subtitlev1alpha1.SubtitleItem) { it.Score++ },
		"scoreOutOf":   func(it *subtitlev1alpha1.SubtitleItem) { it.ScoreOutOf++ },
		"provider":     func(it *subtitlev1alpha1.SubtitleItem) { it.Provider = "gestdown" },
		"subtitleID":   func(it *subtitlev1alpha1.SubtitleItem) { it.SubtitleID = "42" },
		"path":         func(it *subtitlev1alpha1.SubtitleItem) { it.Path = "Movie.en.srt" },
		"lastError":    func(it *subtitlev1alpha1.SubtitleItem) { it.LastError = "boom" },
		"downloadedAt": func(it *subtitlev1alpha1.SubtitleItem) { it.DownloadedAt = at(time.Hour) },
	}
	for leaf, mut := range worker {
		o := base.DeepCopy()
		mut(&o.Status.Items[1])
		assert.NotEqual(t, sig, workerSignal(o), "a worker write of %s must wake the controller", leaf)
	}
	added := base.DeepCopy()
	added.Status.Items = append(added.Status.Items, subtitlev1alpha1.SubtitleItem{LangKey: "de", State: subtitlev1alpha1.SubtitleItemSearching})
	assert.NotEqual(t, sig, workerSignal(added), "the worker creating an entry must wake the controller")

	own := base.DeepCopy()
	own.Status.Phase = subtitlev1alpha1.SubtitleRequestPhaseSearching
	own.Status.ObservedGeneration = 9
	own.Status.ProbeHash = "x"
	own.Status.Existing = []subtitlev1alpha1.ExistingSub{{LangKey: "en", Source: subtitlev1alpha1.SubtitleSourceSidecar}}
	own.Status.Items[0].Attempts = commonv1alpha1.Attempts{Count: 3, Latest: at(0)}
	own.Status.Items[0].NextSearchAt = at(time.Hour)
	assert.Equal(t, sig, workerSignal(own), "this controller's own writes must not re-trigger it")

	reordered := base.DeepCopy()
	reordered.Status.Items[0], reordered.Status.Items[1] = reordered.Status.Items[1], reordered.Status.Items[0]
	assert.Equal(t, sig, workerSignal(reordered), "list order is not a change")

	var notARequest client.Object = &subtitlev1alpha1.SubtitleProfile{}
	assert.Empty(t, workerSignal(notARequest))
}
