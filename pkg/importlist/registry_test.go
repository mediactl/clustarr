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

package importlist_test

import (
	"context"
	"errors"
	"testing"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeList struct {
	name  string
	kind  commonv1.MediaKind
	items []importlist.Item
	err   error
}

func (f *fakeList) Name() string             { return f.name }
func (f *fakeList) Kind() commonv1.MediaKind { return f.kind }
func (f *fakeList) Fetch(context.Context) ([]importlist.Item, error) {
	return f.items, f.err
}

func TestRegistryRegisterGetNames(t *testing.T) {
	r := importlist.NewRegistry()
	a := &fakeList{name: "trakt-watchlist", kind: commonv1.MediaKindMovie}
	b := &fakeList{name: "plex-watchlist", kind: commonv1.MediaKindSeries}

	require.NoError(t, r.Register(a))
	require.NoError(t, r.Register(b))
	assert.ErrorIs(t, r.Register(a), importlist.ErrDuplicateName)

	got, ok := r.Get("trakt-watchlist")
	require.True(t, ok)
	assert.Same(t, importlist.ImportList(a), got)

	assert.Equal(t, []string{"plex-watchlist", "trakt-watchlist"}, r.Names())
}

func TestRegistryFetchAllToleratesOneFailure(t *testing.T) {
	r := importlist.NewRegistry()
	require.NoError(t, r.Register(&fakeList{name: "ok", items: []importlist.Item{{Title: "Dune", Year: 2021}}}))
	require.NoError(t, r.Register(&fakeList{name: "broken", err: errors.New("boom")}))

	results := r.FetchAll(context.Background())

	require.Len(t, results, 2)
	assert.Equal(t, "broken", results[0].Name) // sorted by name
	assert.ErrorContains(t, results[0].Err, "boom")
	assert.Equal(t, "ok", results[1].Name)
	require.Len(t, results[1].Items, 1)
	assert.Equal(t, "Dune", results[1].Items[0].Title)
}
