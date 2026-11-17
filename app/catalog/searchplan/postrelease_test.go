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

package searchplan_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/wantedcron"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
)

// airedView is an Episode whose air date was aired ago, searched live once
// as that day began (the search its date made), its backoff not elapsed.
func airedView(name string, aired time.Duration) searchplan.View {
	v := searchedView(name)
	at := t0.Add(-aired)
	live := metav1.NewTime(at)
	v.ReleasedAt, v.LastLiveSearch = &at, &live
	old := metav1.NewTime(t0.Add(-wantedcron.MinimumGap - time.Hour))
	v.Dispatch = &catalogv1alpha1.Dispatch{Seq: 7, AnsweredSeq: 7, DispatchedAt: old}
	return v
}

// TestAWantedItemIsSearchedLiveAgainAfterItsReleaseDay: an air date is a
// day read as 00:00 UTC, so the live search it makes runs before the
// episode airs; one more is owed PostReleaseDelay after it, whatever the
// backoff, and until then the pass asks to be woken for it.
func TestAWantedItemIsSearchedLiveAgainAfterItsReleaseDay(t *testing.T) {
	pace := func() time.Time { return t0 }

	early := airedView("ep", searchplan.PostReleaseDelay-2*time.Hour)
	p := searchplan.Decide(early, admitAll, pace)
	require.Nil(t, p.Publish, "not before the delay has passed")
	require.Equal(t, t0.Add(2*time.Hour), p.Due, "the pass is owed again when it has")

	due := airedView("ep", searchplan.PostReleaseDelay+time.Minute)
	p = searchplan.Decide(due, admitAll, pace)
	require.NotNil(t, p.Publish)
	require.True(t, p.Publish.Live)
	require.False(t, p.Publish.Payload.IndexOnly, "it asks the indexers")
	require.NotNil(t, p.LastLiveSearchAt)
	require.Equal(t, t0, p.LastLiveSearchAt.Time)

	t.Run("once", func(t *testing.T) {
		v := airedView("ep", searchplan.PostReleaseDelay+3*time.Hour)
		live := metav1.NewTime(t0.Add(-time.Hour))
		v.LastLiveSearch = &live
		p := searchplan.Decide(v, admitAll, pace)
		require.Nil(t, p.Publish, "a live search since the delay is that search")
		require.True(t, p.Due.IsZero())
	})
	t.Run("behind the pace", func(t *testing.T) {
		v := airedView("ep", searchplan.PostReleaseDelay+time.Minute)
		p := searchplan.Decide(v, admitAll, func() time.Time { return t0.Add(time.Minute) })
		require.Nil(t, p.Publish)
		require.Equal(t, catalogv1alpha1.DeliveryReasonPaced, p.Waiting)
	})
	t.Run("not once it has its file", func(t *testing.T) {
		v := airedView("ep", searchplan.PostReleaseDelay+time.Minute)
		v.Wanted = false
		require.Nil(t, searchplan.Decide(v, admitAll, pace).Publish)
	})
	t.Run("not for an item released long ago", func(t *testing.T) {
		v := airedView("ep", searchplan.PostReleaseWindow+time.Hour)
		p := searchplan.Decide(v, admitAll, pace)
		require.Nil(t, p.Publish, "past the window it is the backlog's")
		require.True(t, p.Due.IsZero())
	})
	t.Run("not for one added unsearched", func(t *testing.T) {
		v := airedView("ep", searchplan.PostReleaseDelay+time.Minute)
		v.NoSearchOnAdd = true
		require.Nil(t, searchplan.Decide(v, admitAll, pace).Publish)
	})
	t.Run("waits for its season", func(t *testing.T) {
		v := airedView("ep", searchplan.PostReleaseDelay+time.Minute)
		v.SeasonPending = true
		p := searchplan.Decide(v, admitAll, pace)
		require.Nil(t, p.Publish)
		require.True(t, p.Due.IsZero(), "the season's answer wakes it; a Due in the past would spin the pass")
	})
	t.Run("after the floor of a dispatch just closed", func(t *testing.T) {
		v := airedView("ep", searchplan.PostReleaseDelay+time.Minute)
		recent := metav1.NewTime(t0.Add(-time.Hour))
		v.Dispatch.DispatchedAt = recent
		p := searchplan.Decide(v, admitAll, pace)
		require.Nil(t, p.Publish)
		require.Equal(t, recent.Add(wantedcron.MinimumGap), p.Due)
	})
	t.Run("an item with no release date gets none", func(t *testing.T) {
		v := airedView("ep", searchplan.PostReleaseDelay+time.Minute)
		v.ReleasedAt = nil
		require.Nil(t, searchplan.Decide(v, admitAll, pace).Publish)
	})
}
