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

package itemread_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/itemread"
)

// A dispatch is a search of the item only once it closed having asked an
// indexer, which is what moves lastSearchedAt: one in flight, one every
// indexer was paced for and one nobody answered leave a redownload owed.
func TestSearchCountedIsAnAnsweredSearchOnly(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 11, 17, 6, 57, 22, 0, time.UTC))
	before := metav1.NewTime(at.Add(-time.Hour))
	after := metav1.NewTime(at.Add(8 * time.Second))
	closed := &catalogv1alpha1.Dispatch{Seq: 7, AnsweredSeq: 7, DispatchedAt: at}

	require.True(t, itemread.SearchCounted(closed, &after), "answered as a search")
	require.True(t, itemread.SearchCounted(closed, &at), "answered within the dispatch's second")
	require.False(t, itemread.SearchCounted(closed, &before), "closed without moving lastSearchedAt: paced, none to ask, or timed out")
	require.False(t, itemread.SearchCounted(closed, nil))
	require.False(t, itemread.SearchCounted(&catalogv1alpha1.Dispatch{Seq: 8, AnsweredSeq: 7, DispatchedAt: at}, &after), "still in flight")
	require.False(t, itemread.SearchCounted(nil, &after))
}
