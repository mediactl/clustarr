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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestARedownloadThatAskedNoIndexerIsOwedAgain: a failed grab's retry is a
// search, and a dispatch every indexer was paced for -- or that nobody
// answered -- is none. It had counted as the failure's search, so the
// retry ended there while releases remained; it is dispatched again after
// RedownloadRetry, the wait growing with the time since the failure, until
// one asks an indexer.
func TestARedownloadThatAskedNoIndexerIsOwedAgain(t *testing.T) {
	failedAt := t0.Add(-time.Minute)
	v := itemView("succession-s03e01", t0)
	v.Attempts = commonv1.Attempts{Count: 2, Initial: ago(2 * time.Hour), Latest: ago(time.Hour)}
	v.LastSearched = v.Attempts.Latest
	v.Dispatch = &catalogv1alpha1.Dispatch{Seq: 5, AnsweredSeq: 5, DispatchedAt: *ago(time.Hour)}
	v.Redownload = &failedAt

	first := searchplan.Decide(v, admitAll, func() time.Time { return t0 })
	require.NotNil(t, first.Publish)
	require.Equal(t, schema.SearchReasonRedownload, first.Publish.Payload.Reason)

	// Every indexer was paced: the dispatch closes, uncounted.
	paced := carry(v, first, t0.Add(30*time.Second))
	paced.Record = answer(first, t0.Add(20*time.Second), true)
	p := searchplan.Decide(paced, admitAll, func() time.Time { return paced.Now })
	require.Nil(t, p.Publish, "not straight back at the indexers")
	require.Equal(t, t0.Add(searchplan.RedownloadRetry), p.Due, "the pass is owed again when the retry is")
	require.Equal(t, v.LastSearched, p.LastSearchedAt, "it was no search")

	retry := carry(paced, p, t0.Add(searchplan.RedownloadRetry))
	retry.Record, retry.InFlight = nil, nil
	require.True(t, searchplan.RedownloadDue(retry, retry.Now))
	second := searchplan.Decide(retry, admitAll, func() time.Time { return retry.Now })
	require.NotNil(t, second.Publish, "the redownload is dispatched again")
	require.Equal(t, schema.SearchReasonRedownload, second.Publish.Payload.Reason)
	require.True(t, second.Publish.Live)

	// The second asks the indexers: the failure owes no more.
	done := carry(retry, second, retry.Now.Add(time.Minute))
	done.Record = answer(second, retry.Now.Add(30*time.Second), false)
	p = searchplan.Decide(done, admitAll, func() time.Time { return done.Now })
	require.Nil(t, p.Publish)
	require.True(t, p.Due.IsZero())
	after := carry(done, p, done.Now.Add(2*time.Hour))
	after.Record, after.InFlight = nil, nil
	require.False(t, searchplan.RedownloadDue(after, after.Now), "a search that asked the indexers is the failure's")

	t.Run("the wait grows with the time since the failure", func(t *testing.T) {
		v := itemView("ep", t0)
		failed := t0.Add(-3 * time.Hour)
		v.Redownload = &failed
		v.LastSearched = ago(4 * time.Hour)
		v.Attempts = commonv1.Attempts{Count: 1, Latest: v.LastSearched}
		// Dispatched 40 minutes after the failure, closed uncounted.
		v.Dispatch = &catalogv1alpha1.Dispatch{Seq: 9, AnsweredSeq: 9, DispatchedAt: *ago(140 * time.Minute)}
		require.True(t, searchplan.RedownloadDue(v, t0), "40 minutes after a dispatch 40 minutes after the failure")

		v.Dispatch.DispatchedAt = *ago(30 * time.Minute)
		require.False(t, searchplan.RedownloadDue(v, t0), "an hour at most, and half of it has passed")
		p := searchplan.Decide(v, admitAll, func() time.Time { return t0 })
		require.Equal(t, t0.Add(30*time.Minute), p.Due)
	})
	t.Run("not once the failure is a day old", func(t *testing.T) {
		v := itemView("ep", t0)
		failed := t0.Add(-25 * time.Hour)
		v.Redownload = &failed
		v.LastSearched = ago(30 * time.Hour)
		v.Attempts = commonv1.Attempts{Count: 1, Latest: v.LastSearched}
		v.Dispatch = &catalogv1alpha1.Dispatch{Seq: 9, AnsweredSeq: 9, DispatchedAt: *ago(2 * time.Hour)}
		require.False(t, searchplan.RedownloadDue(v, t0))
	})
}
