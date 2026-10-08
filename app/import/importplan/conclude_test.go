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

package importplan_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/import/importplan"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestATransientImportRetriesOnStatusNotStreamState (A3.8, P68): an import
// that fails for a passing reason is retried at 1 m, 10 m and 45 m, counted
// by the entry's import summary -- attempts and nextAttemptAt, which any
// manager replica reads back -- not by the import consumer's redelivery (the
// agent answers every attempt on a first delivery). After the third retry it
// is held for a person, never blocklisted. Before nextAttemptAt nothing is
// owed, and a summary restored mid-ladder carries on from where it stood.
func TestATransientImportRetriesOnStatusNotStreamState(t *testing.T) {
	wd := newWorld(t)
	notYet := filepath.Join(wd.dir, "downloads", "not-yet") // the content root is not mounted yet
	g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", notYet)

	for attempt, wait := range importplan.RetryLadder {
		dec := g.inspected()
		require.Equal(t, lifecycle.ImportRetry, dec.Verdict, "attempt %d: %s", attempt+1, dec.Summary.Message)
		assert.Equal(t, commonv1.ImportClassTransient, dec.Class)
		assert.Equal(t, catalogv1alpha1.ImportPhasePending, dec.Summary.Phase)
		assert.EqualValues(t, attempt+1, dec.Summary.Attempts)
		require.NotNil(t, dec.Summary.NextAttemptAt)
		next := wd.now.Add(wait)
		assert.Equal(t, next, dec.Summary.NextAttemptAt.UTC(), "attempt %d waits %s", attempt+1, wait)
		assert.Equal(t, next, dec.Due)
		assert.Contains(t, dec.Summary.Message, fmt.Sprintf("retrying at %s (attempt %d of %d)",
			next.Format(time.RFC3339), attempt+2, len(importplan.RetryLadder)+1), "the retry names itself")

		// Until then nothing is owed, and nothing changes.
		wd.now = next.Add(-time.Second)
		waiting := g.decide()
		assert.Equal(t, lifecycle.ImportRetry, waiting.Verdict)
		assert.Nil(t, waiting.Dispatch, "no task before nextAttemptAt")
		assert.Equal(t, next, waiting.Due)
		assert.EqualValues(t, attempt+1, waiting.Summary.Attempts)
		wd.now = next
	}
	dec := g.inspected()
	assert.Equal(t, lifecycle.ImportHeld, dec.Verdict, dec.Summary.Message)
	assert.Equal(t, catalogv1alpha1.ImportPhaseHeld, dec.Summary.Phase)
	assert.Equal(t, commonv1.ImportClassTransient, dec.Class)
	assert.Nil(t, dec.Summary.NextAttemptAt)
	require.NotNil(t, dec.Summary.HeldSince)
	assert.Equal(t, wd.now, dec.Summary.HeldSince.UTC())
	assert.Nil(t, dec.Dispatch)
	assert.EqualValues(t, len(importplan.RetryLadder)+1, dec.Summary.Attempts, "a first try and three retries")

	// A summary read back mid-ladder -- another replica, a restart --
	// carries on from its attempts: the next failure waits the third rung,
	// and a payload that has turned up meanwhile imports.
	h := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", notYet)
	due := metav1.NewTime(wd.now.Add(-time.Second))
	h.dec.Summary = catalogv1alpha1.DownloadImportSummary{
		Phase: catalogv1alpha1.ImportPhasePending, Class: commonv1.ImportClassTransient, Attempts: 2, NextAttemptAt: &due,
		Dispatch: catalogv1alpha1.Dispatch{Seq: 41, AnsweredSeq: 41, DispatchedAt: metav1.NewTime(wd.now.Add(-time.Hour))},
	}
	dec = h.inspected()
	require.Equal(t, lifecycle.ImportRetry, dec.Verdict)
	assert.Equal(t, wd.now.Add(importplan.RetryLadder[2]), dec.Summary.NextAttemptAt.UTC(), "the third retry's wait")

	wd.now = dec.Summary.NextAttemptAt.UTC()
	require.NoError(t, os.MkdirAll(notYet, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(notYet, "Heat.1995.1080p.BluRay.x264-GRP.mkv"), nil, 0o644))
	require.NoError(t, os.Truncate(filepath.Join(notYet, "Heat.1995.1080p.BluRay.x264-GRP.mkv"), big))
	h.inspected()
	assert.Len(t, h.plan().Plan.Moves, 1, "the payload is there now")
}

// TestAHeldImportExpiresAfterTwentyFourHoursAndIsNeverBlocklisted (A3.8,
// P68): an import held for a person keeps its files for
// lifecycle.ImportHoldRetention (24 h) from heldSince -- owing nothing, due
// again at the expiry -- and then expires: the entry fails as
// importExpired, which lifecycle never blocklists and never searches again.
// An expired import stays expired, and a held summary that lost its
// heldSince starts its 24 hours now rather than expiring at once.
func TestAHeldImportExpiresAfterTwentyFourHoursAndIsNeverBlocklisted(t *testing.T) {
	require.Equal(t, 24*time.Hour, lifecycle.ImportHoldRetention)
	wd := newWorld(t)
	// A lone suspected sample: a person's call (P55).
	root := wd.payload(map[string]int64{"Heat.1995.1080p.BluRay.x264-GRP.mkv": small})
	g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", root)
	start := wd.now
	dec := g.inspected()
	require.Equal(t, lifecycle.ImportHeld, dec.Verdict, dec.Summary.Message)
	require.NotNil(t, dec.Summary.HeldSince)
	assert.Equal(t, start, dec.Summary.HeldSince.UTC())
	assert.Equal(t, start.Add(lifecycle.ImportHoldRetention), dec.Due)

	wd.now = start.Add(lifecycle.ImportHoldRetention - time.Second)
	dec = g.decide()
	assert.Equal(t, lifecycle.ImportHeld, dec.Verdict, "still inside its 24 hours")
	assert.Equal(t, catalogv1alpha1.ImportPhaseHeld, dec.Summary.Phase)
	assert.Nil(t, dec.Dispatch)
	assert.Equal(t, start.Add(lifecycle.ImportHoldRetention), dec.Due)

	wd.now = start.Add(lifecycle.ImportHoldRetention)
	dec = g.decide()
	assert.Equal(t, lifecycle.ImportExpired, dec.Verdict)
	assert.Equal(t, catalogv1alpha1.ImportPhaseExpired, dec.Summary.Phase)
	assert.Equal(t, commonv1.ImportClassNeedsPerson, dec.Summary.Class, "the class it was held for")
	assert.NotEqual(t, lifecycle.ImportReleaseFault, dec.Verdict, "an expired hold is never blocklisted")
	assert.Nil(t, dec.Dispatch)
	assert.Empty(t, dec.Materialise)

	wd.now = wd.now.Add(7 * 24 * time.Hour)
	dec = g.decide()
	assert.Equal(t, lifecycle.ImportExpired, dec.Verdict, "an expired import stays expired")
	assert.Nil(t, dec.Dispatch)

	// A held summary without heldSince starts its hold now.
	h := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", root)
	h.dec.Summary = catalogv1alpha1.DownloadImportSummary{
		Phase: catalogv1alpha1.ImportPhaseHeld, Class: commonv1.ImportClassItemState,
		Dispatch: catalogv1alpha1.Dispatch{Seq: 41, AnsweredSeq: 41, DispatchedAt: metav1.NewTime(wd.now.Add(-48 * time.Hour))},
	}
	dec = h.decide()
	assert.Equal(t, lifecycle.ImportHeld, dec.Verdict)
	require.NotNil(t, dec.Summary.HeldSince)
	assert.Equal(t, wd.now, dec.Summary.HeldSince.UTC())

	// A person's import intent is the way out of a hold.
	h.in.Intent = &lifecycle.ImportIntent{ID: h.in.Entry.ID, Override: true}
	dec = h.decide()
	require.NotNil(t, dec.Dispatch)
	assert.Equal(t, schema.ImportSubInspect, dec.Dispatch.Phase)
	assert.Equal(t, catalogv1alpha1.ImportPhasePending, dec.Summary.Phase)
}
