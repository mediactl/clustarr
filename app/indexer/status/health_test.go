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

package status

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
)

func TestEscalationTableIsProwlarrsTenEntryLadder(t *testing.T) {
	want := []time.Duration{
		0, time.Minute, 5 * time.Minute, 15 * time.Minute,
		30 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour,
		12 * time.Hour, 24 * time.Hour,
	}
	require.Equal(t, want, EscalationTable())
	require.Len(t, EscalationTable(), 10, "level is capped at len-1 = 9")

	got := EscalationTable()
	got[0] = time.Hour
	require.Equal(t, time.Duration(0), EscalationTable()[0], "EscalationTable must return a copy")
}

func TestRecordFailureEscalatesAndCapsAtNine(t *testing.T) {
	processStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Cleanup(func() { processStart = time.Now() })
	now := processStart.Add(time.Hour) // well outside the grace

	cases := []struct {
		from int32
		want int32
		dur  time.Duration
	}{
		{from: 0, want: 1, dur: time.Minute},
		{from: 4, want: 5, dur: time.Hour},
		{from: 8, want: 9, dur: 24 * time.Hour},
		{from: 9, want: 9, dur: 24 * time.Hour},
		{from: 40, want: 9, dur: 24 * time.Hour}, // a corrupted status cannot index out of range
	}
	for _, tc := range cases {
		got := RecordFailure(indexv1alpha1.IndexerStatus{EscalationLevel: tc.from}, now, "boom")
		require.Equal(t, tc.want, got.FailureLevel)
		require.True(t, got.Changed)
		require.NotNil(t, got.DisabledUntil)
		require.Equal(t, now.Add(tc.dur), got.DisabledUntil.Time)
		require.Equal(t, "boom", got.LastFailureMsg)
	}
}

func TestRecordFailureInsideStartupGraceDoesNotEscalate(t *testing.T) {
	processStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Cleanup(func() { processStart = time.Now() })

	inside := processStart.Add(StartupGrace - time.Second)
	got := RecordFailure(indexv1alpha1.IndexerStatus{EscalationLevel: 3}, inside, "boom")
	require.Equal(t, int32(3), got.FailureLevel, "grace must not escalate")
	require.True(t, got.Changed, "the failure is still recorded")

	atBoundary := processStart.Add(StartupGrace)
	require.Equal(t, int32(4), RecordFailure(indexv1alpha1.IndexerStatus{EscalationLevel: 3}, atBoundary, "boom").FailureLevel)
}

func TestRecordFailureAtLevelZeroDoesNotDisable(t *testing.T) {
	processStart = time.Now()
	t.Cleanup(func() { processStart = time.Now() })
	got := RecordFailure(indexv1alpha1.IndexerStatus{}, processStart, "boom")
	require.Equal(t, int32(0), got.FailureLevel)
	require.Nil(t, got.DisabledUntil, "escalationTable[0] is 0s, which is not a disable")
}

func TestRecordFailureTruncatesTheMessage(t *testing.T) {
	processStart = time.Now()
	t.Cleanup(func() { processStart = time.Now() })
	got := RecordFailure(indexv1alpha1.IndexerStatus{}, processStart, strings.Repeat("x", 4096))
	require.Len(t, got.LastFailureMsg, maxFailureMsg)
}

// A message cut mid-rune is invalid UTF-8, which the apiserver rejects
// outright: the indexer's long error would become "this object's status can
// no longer be written at all".
func TestRecordFailureTruncatesOnARuneBoundary(t *testing.T) {
	processStart = time.Now()
	t.Cleanup(func() { processStart = time.Now() })
	// 512 is not a multiple of 3, so a naive s[:512] splits a rune.
	got := RecordFailure(indexv1alpha1.IndexerStatus{}, processStart, strings.Repeat("\u2713", 1000))
	require.True(t, utf8.ValidString(got.LastFailureMsg), "truncation split a multi-byte rune")
	require.LessOrEqual(t, len(got.LastFailureMsg), maxFailureMsg)
}

func TestRecordFailureInitialFailureStartsAndCarriesTheRun(t *testing.T) {
	processStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Cleanup(func() { processStart = time.Now() })
	now := processStart.Add(time.Hour)

	first := RecordFailure(indexv1alpha1.IndexerStatus{}, now, "boom")
	require.Equal(t, first.LastFailureAt, first.InitialFailure(indexv1alpha1.IndexerStatus{}),
		"the first failure of a run starts the run")

	began := metav1.NewTime(now.Add(-time.Hour))
	cur := indexv1alpha1.IndexerStatus{EscalationLevel: 2, InitialFailureAt: &began}
	second := RecordFailure(cur, now, "boom again")
	require.Equal(t, &began, second.InitialFailure(cur), "an ongoing run keeps its start")
}

// The brief's RecordFailure clamped only through min(level+1, max), which is
// never reached inside the startup grace: a status carrying level 40 -- an
// older build, or a hand edit -- would index escalationTable[40] and panic
// the whole reconcile. The clamp is therefore applied to the incoming level
// before the grace check, not only to the escalated one.
func TestRecordFailureClampsACorruptedLevelInsideTheGraceToo(t *testing.T) {
	processStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Cleanup(func() { processStart = time.Now() })

	inside := processStart.Add(StartupGrace - time.Second)
	require.NotPanics(t, func() {
		got := RecordFailure(indexv1alpha1.IndexerStatus{EscalationLevel: 40}, inside, "boom")
		require.Equal(t, int32(9), got.FailureLevel)
	})
}

func TestRecordSuccessOnAHealthyIndexerIsANoOp(t *testing.T) {
	got := RecordSuccess(indexv1alpha1.IndexerStatus{}, time.Now())
	require.Equal(t, Escalation{}, got)
	require.False(t, got.Changed, "a caller must be able to skip the apply entirely")
}

func TestRecordSuccessDeEscalatesOneStepAndClearsTheDisable(t *testing.T) {
	now := time.Now()
	until := metav1.NewTime(now.Add(time.Hour))
	at := metav1.NewTime(now.Add(-time.Minute))
	cur := indexv1alpha1.IndexerStatus{
		EscalationLevel: 5, DisabledUntil: &until,
		InitialFailureAt: &at, LastFailureAt: &at, LastFailure: "boom",
	}
	got := RecordSuccess(cur, now)
	require.True(t, got.Changed)
	require.Equal(t, int32(4), got.FailureLevel)
	require.Nil(t, got.DisabledUntil)
	require.Equal(t, &at, got.LastFailureAt, "history is carried, not dropped, while the level is > 0")
	require.Equal(t, "boom", got.LastFailureMsg)
	require.Equal(t, &at, got.InitialFailure(cur))
}

func TestRecordSuccessAtLevelOneClearsTheWholeRun(t *testing.T) {
	now := time.Now()
	at := metav1.NewTime(now.Add(-time.Minute))
	cur := indexv1alpha1.IndexerStatus{EscalationLevel: 1, InitialFailureAt: &at, LastFailureAt: &at, LastFailure: "boom"}
	got := RecordSuccess(cur, now)
	require.True(t, got.Changed)
	require.Equal(t, int32(0), got.FailureLevel)
	require.Nil(t, got.DisabledUntil)
	require.Nil(t, got.LastFailureAt)
	require.Empty(t, got.LastFailureMsg)
	require.Nil(t, got.InitialFailure(cur), "the run is over")
}

// A success while the level is already 0 but a stale disable or message is
// still on the object must still clear them, or the object stays False
// forever with nothing to move it.
func TestRecordSuccessClearsLeftoversAtLevelZero(t *testing.T) {
	now := time.Now()
	until := metav1.NewTime(now.Add(time.Hour))
	cur := indexv1alpha1.IndexerStatus{DisabledUntil: &until, LastFailure: "boom"}
	got := RecordSuccess(cur, now)
	require.True(t, got.Changed)
	require.Equal(t, int32(0), got.FailureLevel)
	require.Nil(t, got.DisabledUntil)
	require.Empty(t, got.LastFailureMsg)
}

func TestHealthy(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	past := metav1.NewTime(now.Add(-time.Second))
	future := metav1.NewTime(now.Add(time.Hour))
	require.True(t, Healthy(indexv1alpha1.IndexerStatus{}, now), "never-failed is healthy")
	require.True(t, Healthy(indexv1alpha1.IndexerStatus{EscalationLevel: 7}, now), "a level with no disable is still queryable")
	require.True(t, Healthy(indexv1alpha1.IndexerStatus{DisabledUntil: &past}, now))
	require.False(t, Healthy(indexv1alpha1.IndexerStatus{DisabledUntil: &future}, now))
	require.True(t, Healthy(indexv1alpha1.IndexerStatus{DisabledUntil: &metav1.Time{Time: now}}, now), "the boundary re-enables")
}

// A never-probed Indexer carries status.caps == nil. SupportsMode's contract
// is that a zero Caps supports NOTHING; a caller reading it as "supports
// everything" would query an indexer that has never answered.
func TestSupportsModeOnAnUnprobedIndexer(t *testing.T) {
	require.False(t, SupportsMode(indexv1alpha1.Caps{}, "search"))
	require.False(t, SupportsMode(indexv1alpha1.Caps{Modes: map[string][]string{}}, "search"))
}
