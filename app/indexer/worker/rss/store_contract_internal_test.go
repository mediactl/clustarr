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

package rss

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/relindex"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// torznabTitled is the smallest wire release that exercises the projection.
func torznabTitled(title, guid string) torznab.Release {
	return torznab.Release{Title: title, GUID: guid}
}

// TestRejectReasonAgreesWithTheRealStore is the reason rejectReason is
// allowed to mirror relindex's validate at all.
//
// A guard that restates another package's rules in prose drifts the moment
// that package gains a rule, and the failure mode here is expensive: Upsert
// validates the WHOLE batch before opening its transaction, so one row the
// filter missed loses the entire page and, because indexAndPublish returns
// before both the status write and ScheduleNext, ends that indexer's RSS
// chain for good with the Indexer still reading healthy.
//
// So the mirror is checked against a REAL sqlite store, in both directions,
// and the cases are driven by REFLECTION over relindex.Release rather than
// by a hand-written list. That distinction is the whole value of the test: a
// hand-written table only varies the dimensions someone thought to vary, so
// a new rule on a field the table never zeroes passes unnoticed. Zeroing
// every field in turn catches any rule that rejects a zero value -- which is
// how all five of today's rules work -- including on fields this package
// does not currently vary, such as Protocol, which an Indexer genuinely can
// leave empty before its first reconcile.
//
// What it still cannot catch is a rule that rejects some NON-zero value, for
// example a size ceiling. Nothing here claims otherwise.
func TestRejectReasonAgreesWithTheRealStore(t *testing.T) {
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

// assertAgrees holds rejectReason and the real store to the same verdict,
// in both directions.
func assertAgrees(t *testing.T, store relindex.Store, row relindex.Release) {
	t.Helper()
	reason := rejectReason(row)
	_, err := store.Upsert(t.Context(), []relindex.Release{row})
	if reason == "" {
		require.NoError(t, err,
			"rejectReason passed a row the store refuses: ONE of these in a page loses the whole page")
		return
	}
	require.ErrorIs(t, err, relindex.ErrInvalidRelease,
		"rejectReason drops a row the store would have accepted: %s", reason)
}

// The two shapes an indexer can actually put on the wire. Neither is exotic:
// release.TitleNorm keeps no punctuation or symbols, and pkg/torznab does not
// invent a GUID for an item that carries none.
func TestRejectReasonCatchesWhatAFeedCanActuallySend(t *testing.T) {
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for _, title := range []string{"★★★", "???", "「」【】"} {
		t.Run(title, func(t *testing.T) {
			rel := ProjectRelease(torznabTitled(title, "g"), "idx", "torrent")
			rel.FetchedAt = at
			row, err := indexRow(rel, "idx", at)
			require.NoError(t, err)
			require.Empty(t, row.TitleNorm, "premise changed: TitleNorm now keeps something")
			require.NotEmpty(t, rejectReason(row))
		})
	}

	rel := ProjectRelease(torznabTitled("The Matrix 1999 1080p BluRay x264-GRP", ""), "idx", "torrent")
	rel.FetchedAt = at
	row, err := indexRow(rel, "idx", at)
	require.NoError(t, err)
	require.Equal(t, "the indexer reported no guid", rejectReason(row))
}

// A wholly non-Latin title indexes under ITSELF. Under release.CleanTitle
// these normalised to "" and the store refused the row, so a release named
// only in Cyrillic, Japanese or Korean was unsearchable by its own title.
// The row goes to a REAL store and is found by a query built the way
// indexarr/query builds one, so the two sides are proved to agree.
func TestANonLatinTitleIndexesUnderItsOwnTitle(t *testing.T) {
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	store, closer, err := relindex.Open(t.Context(), filepath.Join(t.TempDir(), "releases.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })
	for i, title := range []string{"Матрица", "日本語のタイトル", "마마마"} {
		t.Run(title, func(t *testing.T) {
			rel := ProjectRelease(torznabTitled(title, fmt.Sprintf("g%d", i)), "idx", "torrent")
			rel.FetchedAt = at
			row, err := indexRow(rel, "idx", at)
			require.NoError(t, err)
			require.Equal(t, release.TitleNorm(title), row.TitleNorm)
			require.NotEmpty(t, row.TitleNorm)
			require.Empty(t, rejectReason(row))
			_, err = store.Upsert(t.Context(), []relindex.Release{row})
			require.NoError(t, err)

			got, err := store.Search(t.Context(), relindex.Query{Text: release.TitleNorm(title), Limit: 10})
			require.NoError(t, err)
			require.Len(t, got, 1, "the row is findable by its own title")
			require.Equal(t, row.GUID, got[0].GUID)
		})
	}
}
