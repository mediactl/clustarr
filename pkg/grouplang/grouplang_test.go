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

package grouplang

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
)

// bucket is the release groups bucket on an in-memory bus with the default
// topology ensured.
func bucket(t *testing.T) events.KV {
	t.Helper()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	return bus.KV(events.BucketReleaseGroups)
}

func record(t *testing.T, kv events.KV, group string) Record {
	t.Helper()
	e, err := kv.Get(context.Background(), events.KVKeyToken(group))
	require.NoError(t, err)
	var r Record
	require.NoError(t, json.Unmarshal(e.Value, &r))
	return r
}

// TestObserveCountsEachFileOnce: an import observed again (a redelivery, a
// manager restart) counts once, and only a file carrying one language beside
// English and its original votes.
func TestObserveCountsEachFileOnce(t *testing.T) {
	ctx := context.Background()
	kv := bucket(t)
	require.NoError(t, Observe(ctx, kv, "BiOMA", "a", []string{"pt", "en"}, "en"))
	require.NoError(t, Observe(ctx, kv, "BiOMA", "a", []string{"pt", "en"}, "en"))
	require.NoError(t, Observe(ctx, kv, "bioma", "b", []string{"pt", "en"}, "en"))
	require.NoError(t, Observe(ctx, kv, "BiOMA", "c", []string{"en"}, "en"))
	require.NoError(t, Observe(ctx, kv, "BiOMA", "d", []string{"en", "pt", "es", "fr"}, "en"))
	require.NoError(t, Observe(ctx, kv, "BiOMA", "e", nil, "en"), "unknown audio records nothing")
	r := record(t, kv, "bioma")
	require.Equal(t, 4, r.Files)
	require.Equal(t, map[string]int{"pt": 2}, r.Votes)
	require.Equal(t, 1, r.Multi)
	require.Equal(t, []string{"a", "b", "c", "d"}, r.Seen)
}

// TestVerdict holds the thresholds to the owner's library (2026-10-20).
func TestVerdict(t *testing.T) {
	for name, c := range map[string]struct {
		r    Record
		want string
	}{
		"HdT: 44 of 49 files Turkish":               {Record{Files: 49, Votes: map[string]int{"tr": 44}}, "tr"},
		"BiOMA: 6 of 14 files Portuguese":           {Record{Files: 14, Votes: map[string]int{"pt": 6}}, "pt"},
		"PD: one file is not enough to learn":       {Record{Files: 1, Votes: map[string]int{"pt": 1}}, ""},
		"an anime group's English and Japanese":     {Record{Files: 40}, ""},
		"LOST: 3 of 10 files French is too few":     {Record{Files: 10, Votes: map[string]int{"fr": 3}}, ""},
		"two languages in even measure: no verdict": {Record{Files: 6, Votes: map[string]int{"es": 3, "pt": 3}}, ""},
		"a multi-language group":                    {Record{Files: 3, Multi: 3}, ""},
	} {
		got, ok := c.r.Verdict()
		require.Equal(t, c.want, got, name)
		require.Equal(t, c.want != "", ok, name)
	}
}

func TestExtra(t *testing.T) {
	require.Equal(t, []string{"pt"}, Extra([]string{"pt", "ja"}, "ja"), "Akira: Portuguese beside Japanese")
	require.Nil(t, Extra([]string{"en", "ja"}, "ja"))
	require.Equal(t, []string{"tr"}, Extra([]string{"en", "en", "tr", "tr"}, "en"))
	require.Equal(t, []string{"it"}, Extra([]string{"it"}, "ko"), "a dub that replaced the original")
}

// TestIndexLearnsAGroupAndFallsBackToTheBuiltinTable: the index answers a
// learned verdict, a group it has not learned from the built-in table, and
// any other group not at all.
func TestIndexLearnsAGroupAndFallsBackToTheBuiltinTable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kv := bucket(t)
	ix := NewIndex()
	go func() { _ = ix.Run(ctx, kv) }()

	l, ok := ix.GroupLanguage("PD")
	require.True(t, ok)
	require.Equal(t, "Portuguese", l, "the built-in table")
	_, ok = ix.GroupLanguage("NewGroup")
	require.False(t, ok)

	for i := range 3 {
		require.NoError(t, Observe(ctx, kv, "NewGroup", "f"+strconv.Itoa(i), []string{"ja", "es"}, "ja"))
	}
	require.Eventually(t, func() bool {
		l, ok := ix.GroupLanguage("newgroup")
		return ok && l == "Spanish"
	}, 5*time.Second, 20*time.Millisecond, "learned from three Spanish-and-Japanese files")
}
