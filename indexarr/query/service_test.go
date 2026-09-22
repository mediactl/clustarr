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

package query

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/relindex"
)

type fakeStore struct {
	got   relindex.Query
	calls int
	rows  []relindex.Release
	err   error
}

func (f *fakeStore) Search(_ context.Context, q relindex.Query) ([]relindex.Release, error) {
	f.got = q
	f.calls++
	return f.rows, f.err
}

func (f *fakeStore) Upsert(context.Context, []relindex.Release) (int, error) { return 0, nil }
func (f *fakeStore) Prune(context.Context, time.Time) (int, error)           { return 0, nil }
func (f *fakeStore) Stats(context.Context) (relindex.Stats, error)           { return relindex.Stats{}, nil }

func row(t *testing.T, guid string) relindex.Release {
	t.Helper()
	b, err := json.Marshal(schema.Release{
		Info: commonv1.ReleaseInfo{GUID: guid, Title: guid},
	})
	require.NoError(t, err)
	return relindex.Release{Indexer: "tr", GUID: guid, InfoJSON: b}
}

func TestHandleReturnsAnEmptyResultAsASuccess(t *testing.T) {
	got := (&Service{Store: &fakeStore{}}).
		Handle(context.Background(), schema.QueryRequest{Text: "nothing"})
	require.Empty(t, got.Error, "an empty result set is a success, not a failure")
	require.Empty(t, got.Releases)
	require.Zero(t, got.Total)
}

func TestHandleWithNoStoreAnswersWithAPopulatedError(t *testing.T) {
	got := (&Service{}).Handle(context.Background(), schema.QueryRequest{})
	require.Contains(t, got.Error, "not configured")
}

func TestHandleSurfacesAStoreFailureAsAnError(t *testing.T) {
	got := (&Service{Store: &fakeStore{err: errors.New(`fts5: syntax error near "OR"`)}}).
		Handle(context.Background(), schema.QueryRequest{Text: `foo OR`})
	require.Contains(t, got.Error, "fts5")
	require.Empty(t, got.Releases)
}

// An unknown filter must never reach the store: the request is rejected
// before any I/O happens.
func TestHandleRejectsAnUnknownFilterWithoutTouchingTheStore(t *testing.T) {
	st := &fakeStore{}
	got := (&Service{Store: st}).Handle(context.Background(), schema.QueryRequest{
		Filters: map[string]string{"quality": "1080p"},
	})
	require.Contains(t, got.Error, `"quality"`)
	require.Zero(t, st.calls)
	require.LessOrEqual(t, len(got.Error), maxErrorChars)
}

func TestHandlePagesWithOffsetAndReportsTotal(t *testing.T) {
	st := &fakeStore{rows: []relindex.Release{
		row(t, "a"), row(t, "b"), row(t, "c"), row(t, "d"),
	}}
	got := (&Service{Store: st}).
		Handle(context.Background(), schema.QueryRequest{Limit: 2, Offset: 1})
	require.Empty(t, got.Error)
	require.Len(t, got.Releases, 2)
	require.Equal(t, "b", got.Releases[0].Info.GUID)
	require.Equal(t, "c", got.Releases[1].Info.GUID)
	require.Equal(t, int64(4), got.Total,
		"Total is the matched count before paging -- a LOWER BOUND")
	require.Equal(t, 3, st.got.Limit, "limit+offset was over-fetched")
}

// An offset past the end is an empty page, not an error and not a panic.
func TestHandleWithAnOffsetPastTheEndReturnsNothing(t *testing.T) {
	st := &fakeStore{rows: []relindex.Release{row(t, "a")}}
	got := (&Service{Store: st}).
		Handle(context.Background(), schema.QueryRequest{Limit: 10, Offset: 50})
	require.Empty(t, got.Error)
	require.Empty(t, got.Releases)
	require.Equal(t, int64(1), got.Total)
}

func TestHandleSkipsAnUndecodableRowRatherThanFailingTheQuery(t *testing.T) {
	bad := relindex.Release{Indexer: "tr", GUID: "bad", InfoJSON: []byte("{not json")}
	st := &fakeStore{rows: []relindex.Release{bad, row(t, "good")}}
	got := (&Service{Store: st}).Handle(context.Background(), schema.QueryRequest{})
	require.Empty(t, got.Error)
	require.Len(t, got.Releases, 1)
	require.Equal(t, "good", got.Releases[0].Info.GUID)
}

func TestHandleMakesNoOutboundCall(t *testing.T) {
	// The Service has exactly one dependency and it is the local store. This
	// is a structural assertion of that: if someone adds an HTTP client, a
	// bus or a Kubernetes client to Service, this fails and the reviewer has
	// to justify it against Ruling R4.
	var s Service
	require.Equal(t, 2, reflect.TypeOf(s).NumField(),
		"the query verb reads the local index only: no bus, no HTTP client, no Indexer lookup")
	require.Equal(t, "Store", reflect.TypeOf(s).Field(0).Name)
	require.Equal(t, "Now", reflect.TypeOf(s).Field(1).Name)
}

func TestReplyIsBoundedByTheBrokerPayload(t *testing.T) {
	big, err := json.Marshal(schema.Release{
		Info: commonv1.ReleaseInfo{Title: strings.Repeat("x", 64<<10)},
	})
	require.NoError(t, err)
	rows := make([]relindex.Release, 0, 200)
	for range 200 {
		rows = append(rows, relindex.Release{Indexer: "tr", InfoJSON: big})
	}
	got := (&Service{Store: &fakeStore{rows: rows}}).
		Handle(context.Background(), schema.QueryRequest{Limit: 200})
	enc, err := json.Marshal(got)
	require.NoError(t, err)
	require.Less(t, len(enc), 8<<20, "a QueryResponse is ONE NATS message")
	require.Less(t, len(got.Releases), 200, "the byte budget cut the page short")
	require.Equal(t, int64(200), got.Total, "Total still reports what matched")
}
