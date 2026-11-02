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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/history"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A dispatch whose closing pass lost its DispatchAnswered -- the
// catalog-grab set landed, a later set conflicted, the pass ended before its
// effects -- reads closed in status while the ledger still holds its slot,
// which on a rebuildable durable never lapses (talos, 2026-10-30: sixteen
// such slots held catalog-search-normal's whole budget for three and a half
// days). Every later pass of the item answers it again until the ledger lets
// it go; a dispatch still in flight, or a slot the ledger no longer holds,
// owes nothing.
func TestAClosedDispatchTheLedgerStillHoldsIsAnsweredAgain(t *testing.T) {
	now := time.Date(2026, 10, 30, 10, 13, 0, 0, time.UTC)
	key := dispatch.Key{Kind: "Episode", Namespace: "media", Name: "kingdom-s02e05", Sub: history.DispatchSubSearch}
	ledger := dispatch.New(dispatch.Options{Now: func() time.Time { return now }})
	ledger.Published(searchplan.Durable, key, 7)
	st := &Stage{o: Options{Dispatch: ledger}}
	sv := searchplan.View{Key: key, Item: schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: "media", Name: "kingdom-s02e05"}}, Now: now}
	closed := &catalogv1alpha1.Dispatch{Seq: 7, AnsweredSeq: 7, DispatchedAt: metav1.NewTime(now.Add(-time.Minute))}

	answers := func(plan searchplan.Plan) []remediation.DispatchAnswered {
		var out []remediation.DispatchAnswered
		for _, e := range st.result(sv, plan, "").Effects {
			if a, ok := e.(remediation.DispatchAnswered); ok {
				out = append(out, a)
			}
		}
		return out
	}

	got := answers(searchplan.Plan{Dispatch: closed})
	require.Len(t, got, 1, "the lost answer is sent again")
	assert.Equal(t, remediation.DispatchAnswered{Durable: searchplan.Durable, Ledger: key, Seq: 7}, got[0])

	inFlight := &catalogv1alpha1.Dispatch{Seq: 8, AnsweredSeq: 7, DispatchedAt: metav1.NewTime(now)}
	ledger.Published(searchplan.Durable, key, 8)
	assert.Empty(t, answers(searchplan.Plan{Dispatch: inFlight}), "a dispatch in flight is not answered")

	ledger.Answered(searchplan.Durable, key, 8)
	assert.False(t, ledger.Holds(searchplan.Durable, key, 8))
	assert.Empty(t, answers(searchplan.Plan{Dispatch: &catalogv1alpha1.Dispatch{Seq: 8, AnsweredSeq: 8}}),
		"nothing is owed once the ledger let the slot go")
}

// R25's window runs from the failure, as lifecycle's redownload reads it:
// every Remove re-issues an entry's command and moves its dispatch time on,
// which restarted the search stage's window until it read failedAt too.
func TestTheRedownloadWindowRunsFromTheFailureNotTheLatestDispatch(t *testing.T) {
	now := time.Date(2026, 11, 2, 19, 0, 0, 0, time.UTC)
	failedAt := metav1.NewTime(now.Add(-30 * time.Hour))
	e := catalogv1alpha1.DownloadEntry{
		ID: "heat-1", UID: "u1", Phase: commonv1.DownloadPhaseBlocklisted, FailedAt: &failedAt,
		Dispatch: catalogv1alpha1.Dispatch{Seq: 9, AnsweredSeq: 9, DispatchedAt: metav1.NewTime(now.Add(-time.Hour))},
	}
	movie := &catalogv1alpha1.Movie{}
	assert.Nil(t, redownloadAt([]catalogv1alpha1.DownloadEntry{e}, movie, now), "failed 30 h ago: past the window")

	recent := metav1.NewTime(now.Add(-2 * time.Hour))
	e.FailedAt = &recent
	at := redownloadAt([]catalogv1alpha1.DownloadEntry{e}, movie, now)
	require.NotNil(t, at)
	assert.Equal(t, recent.UTC(), *at)

	e.FailedAt = nil
	at = redownloadAt([]catalogv1alpha1.DownloadEntry{e}, movie, now)
	require.NotNil(t, at, "an entry from before failedAt has only its dispatch time")
	assert.Equal(t, now.Add(-time.Hour), *at)
}

// The audit's keyed reads: an item's searchDispatch in flight, closed, or
// the item gone; a Series' seasonSearch dispatch likewise.
func TestTheLedgerSourcesReadOneKeysDispatch(t *testing.T) {
	ctx := t.Context()
	inFlight := &catalogv1alpha1.Episode{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "live-s01e01"}}
	inFlight.Status.SearchDispatch = &catalogv1alpha1.Dispatch{Seq: 9, AnsweredSeq: 8}
	closed := &catalogv1alpha1.Episode{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "done-s01e01"}}
	closed.Status.SearchDispatch = &catalogv1alpha1.Dispatch{Seq: 9, AnsweredSeq: 9}
	ser := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "kingdom"}}
	ser.Status.SeasonSearch = &catalogv1alpha1.SeasonSearchState{Dispatch: &catalogv1alpha1.Dispatch{Seq: 4, AnsweredSeq: 3}}
	r := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(inFlight, closed, ser).Build()

	key := func(kind, name, sub string) dispatch.Key {
		return dispatch.Key{Kind: kind, Namespace: "media", Name: name, Sub: sub}
	}
	for _, tc := range []struct {
		src  dispatch.KeyedSource
		k    dispatch.Key
		seq  int64
		want bool
	}{
		{LedgerSource{}, key("Episode", "live-s01e01", history.DispatchSubSearch), 9, true},
		{LedgerSource{}, key("Episode", "done-s01e01", history.DispatchSubSearch), 0, false},
		{LedgerSource{}, key("Episode", "deleted-s01e01", history.DispatchSubSearch), 0, false},
		{SeasonLedgerSource{}, key("Series", "kingdom", history.DispatchSubSeasonSearch), 4, true},
		{SeasonLedgerSource{}, key("Series", "gone", history.DispatchSubSeasonSearch), 0, false},
	} {
		seq, in, err := tc.src.InFlight(ctx, r, tc.k)
		require.NoError(t, err, tc.k.String())
		assert.Equal(t, tc.want, in, tc.k.String())
		assert.Equal(t, tc.seq, seq, tc.k.String())
	}
}
