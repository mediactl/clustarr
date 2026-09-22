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
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/relindex"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// TestIndexRejectReasonAgreesWithTheRealStore is the reason
// indexRejectReason is allowed to mirror relindex's validate at all.
//
// A guard that restates another package's rules in prose drifts the moment
// that package gains a rule, and the failure mode is expensive: Upsert
// validates the WHOLE batch before opening its transaction, so one row the
// filter missed loses every good release beside it -- silently, because the
// search path treats an index failure as non-fatal by design.
//
// So the mirror is checked against a REAL sqlite store, in both directions,
// and the cases are driven by REFLECTION over relindex.Release rather than
// by a hand-written list. That distinction is the whole value of the test: a
// hand-written table only varies the dimensions someone thought to vary, so
// a new rule on a field the table never zeroes passes unnoticed. The
// previous version of this test did exactly that -- five hand-listed cases,
// no store -- and adding a `case r.Group == "":` rule to relindex.validate
// failed indexarr/worker/rss while leaving this package green.
//
// What it still cannot catch is a rule that rejects some NON-zero value, for
// example a size ceiling. Nothing here claims otherwise.
func TestIndexRejectReasonAgreesWithTheRealStore(t *testing.T) {
	store, closer, err := relindex.Open(t.Context(), filepath.Join(t.TempDir(), "releases.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })

	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	base := relindex.Release{
		Indexer: "idx", GUID: "g-base", Title: "The Matrix 1999",
		TitleNorm: "matrix 1999", Group: "GRP", Protocol: "torrent",
		Categories: []int{2040}, SizeBytes: 1024,
		PublishedAt: ptr.To(at.Add(-time.Hour)), FetchedAt: at,
		InfoJSON: []byte(`{}`),
	}

	// Every field is populated, or zeroing one of them would prove nothing.
	typ := reflect.TypeOf(base)
	baseVal := reflect.ValueOf(base)
	require.NotZero(t, typ.NumField())
	for i := range typ.NumField() {
		require.False(t, baseVal.Field(i).IsZero(),
			"the baseline leaves %s at its zero value, so zeroing it tests nothing", typ.Field(i).Name)
	}
	assertAgrees(t, store, base)

	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		t.Run("zero "+f.Name, func(t *testing.T) {
			row := base
			row.GUID = "g-zero-" + f.Name // distinct, unless GUID is the field being zeroed
			reflect.ValueOf(&row).Elem().Field(i).SetZero()
			assertAgrees(t, store, row)
		})
	}

	// The one shape zeroing cannot express: a non-nil pointer AT the zero
	// time, which is the Phase C PublishedAt defect in a new costume.
	t.Run("a pointer to the zero time", func(t *testing.T) {
		row := base
		row.GUID = "g-zero-ptr"
		row.PublishedAt = ptr.To(time.Time{})
		assertAgrees(t, store, row)
	})
}

// assertAgrees holds indexRejectReason and the real store to the same
// verdict, in both directions.
func assertAgrees(t *testing.T, store relindex.Store, row relindex.Release) {
	t.Helper()
	reason := indexRejectReason(row)
	_, err := store.Upsert(t.Context(), []relindex.Release{row})
	if reason == "" {
		require.NoError(t, err,
			"indexRejectReason passed a row the store refuses: ONE of these in a page loses the whole page")
		return
	}
	require.ErrorIs(t, err, relindex.ErrInvalidRelease,
		"indexRejectReason drops a row the store would have accepted: %s", reason)
}

// The two shapes an indexer can actually put on the wire, driven through the
// real projection rather than a hand-built row. Neither is exotic:
// release.CleanTitle keeps only [a-z0-9 ], and pkg/torznab does not invent a
// GUID for an item that carries none.
func TestIndexRowCatchesWhatAFeedCanActuallySend(t *testing.T) {
	s := &Service{Now: func() time.Time { return time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) }}
	idx := healthyIndexer("idx")

	for _, title := range []string{"★★★", "???", "日本語のタイトル", "「」【】"} {
		t.Run(title, func(t *testing.T) {
			rels := s.project(&idx, []torznab.Release{{Title: title, GUID: "g"}})
			require.Len(t, rels, 1)
			row, err := indexRow(rels[0], "idx")
			require.NoError(t, err)
			require.Empty(t, row.TitleNorm, "premise changed: CleanTitle now keeps something")
			require.NotEmpty(t, indexRejectReason(row))
		})
	}

	rels := s.project(&idx, []torznab.Release{{Title: "The Matrix 1999 1080p BluRay x264-GRP"}})
	row, err := indexRow(rels[0], "idx")
	require.NoError(t, err)
	require.Equal(t, "the indexer reported no guid", indexRejectReason(row))
}

// FetchedAt is stamped by the projection, and relindex refuses a zero one.
// The search path is the only place that stamps it for a search, so a
// regression there loses every row of every search silently.
func TestIndexRowCarriesTheProjectionsFetchedAt(t *testing.T) {
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	row, err := indexRow(schema.Release{
		Info:      commonv1.ReleaseInfo{GUID: "g", Title: "The Matrix 1999"},
		FetchedAt: at,
	}, "idx")
	require.NoError(t, err)
	require.Equal(t, at, row.FetchedAt)
	require.Empty(t, indexRejectReason(row))

	// ... and an absent publish date stays absent rather than becoming a
	// date that sorts as ancient.
	require.Nil(t, row.PublishedAt)
	dated, err := indexRow(schema.Release{
		Info: commonv1.ReleaseInfo{
			GUID: "g", Title: "The Matrix 1999",
			PublishedAt: ptr.To(metav1.NewTime(at.Add(-time.Hour))),
		},
		FetchedAt: at,
	}, "idx")
	require.NoError(t, err)
	require.NotNil(t, dated.PublishedAt)
	require.Empty(t, indexRejectReason(dated))
}
