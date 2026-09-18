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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/importlist"
)

func TestItemKeyPrecedence(t *testing.T) {
	tests := map[string]struct {
		item importlist.Item
		want string
	}{
		"tmdb beats imdb and tvdb": {
			item: importlist.Item{
				Title: "Deadpool", Year: 2016,
				ExternalIDs: importlist.ExternalIDs{TMDB: "293660", IMDb: "tt1431045", TVDB: "12345"},
			},
			want: "tmdb:293660",
		},
		"imdb beats tvdb when no tmdb": {
			item: importlist.Item{
				Title: "Deadpool", Year: 2016,
				ExternalIDs: importlist.ExternalIDs{IMDb: "tt1431045", TVDB: "12345"},
			},
			want: "imdb:tt1431045",
		},
		"tvdb used when no tmdb or imdb": {
			item: importlist.Item{
				Title: "Breaking Bad", Year: 2008,
				ExternalIDs: importlist.ExternalIDs{TVDB: "81189"},
			},
			want: "tvdb:81189",
		},
		"musicbrainz used ahead of the title fallback": {
			item: importlist.Item{
				Title: "Kind of Blue", Year: 1959,
				ExternalIDs: importlist.ExternalIDs{MusicBrainz: "mb-1"},
			},
			want: "musicbrainz:mb-1",
		},
		"title+year fallback when no IDs at all": {
			item: importlist.Item{Title: "The Matrix", Year: 1999},
			want: "title:the matrix:1999",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.item.Key())
		})
	}
}

func TestItemKeyTitleNormalizationEquivalence(t *testing.T) {
	withYearInTitle := importlist.Item{Title: "The Matrix (1999)", Year: 1999}
	withoutYearInTitle := importlist.Item{Title: "the matrix", Year: 1999}

	assert.Equal(t, "title:the matrix:1999", withYearInTitle.Key())
	assert.Equal(t, withYearInTitle.Key(), withoutYearInTitle.Key())
}

func TestItemKeyTitleNormalizationCollapsesPunctuation(t *testing.T) {
	item := importlist.Item{Title: "  Se7en:  The Movie!! ", Year: 1995}
	assert.Equal(t, "title:se7en the movie:1995", item.Key())
}

func TestDedupeKeepsFirstAndPreservesOrder(t *testing.T) {
	a := importlist.Item{Title: "Dune", Year: 2021, ExternalIDs: importlist.ExternalIDs{TMDB: "438631"}}
	aDup := importlist.Item{Title: "Dune (dup)", Year: 2021, ExternalIDs: importlist.ExternalIDs{TMDB: "438631"}}
	b := importlist.Item{Title: "Arrival", Year: 2016, ExternalIDs: importlist.ExternalIDs{IMDb: "tt2543164"}}
	c := importlist.Item{Title: "The Matrix", Year: 1999}
	cDup := importlist.Item{Title: "the matrix", Year: 1999}

	got := importlist.Dedupe([]importlist.Item{a, aDup, b, c, cDup})

	require.Len(t, got, 3)
	assert.Equal(t, "Dune", got[0].Title)
	assert.Equal(t, "Arrival", got[1].Title)
	assert.Equal(t, "The Matrix", got[2].Title)
}

func TestDedupeEmptyAndNilInputs(t *testing.T) {
	assert.Empty(t, importlist.Dedupe(nil))
	assert.Empty(t, importlist.Dedupe([]importlist.Item{}))
}

func TestApplySyncLevelPerLevel(t *testing.T) {
	kept := importlist.Item{Title: "Kept", Year: 2020, ExternalIDs: importlist.ExternalIDs{TMDB: "1"}}
	fellOff := importlist.Item{Title: "Fell Off", Year: 2019, ExternalIDs: importlist.ExternalIDs{TMDB: "2"}}
	current := []importlist.Item{kept}           // "Fell Off" is no longer on the remote list
	existing := []importlist.Item{kept, fellOff} // the catalog previously had both

	tests := map[string]struct {
		level      importlist.SyncLevel
		wantAction importlist.SyncAction
		wantEmpty  bool
	}{
		"disabled takes no action at all":     {level: importlist.SyncLevelDisabled, wantEmpty: true},
		"logOnly logs":                        {level: importlist.SyncLevelLogOnly, wantAction: importlist.SyncActionLog},
		"keepAndUnmonitor unmonitors":         {level: importlist.SyncLevelKeepAndUnmonitor, wantAction: importlist.SyncActionUnmonitor},
		"removeAndKeep removes":               {level: importlist.SyncLevelRemoveAndKeep, wantAction: importlist.SyncActionRemove},
		"removeAndDelete removes and deletes": {level: importlist.SyncLevelRemoveAndDelete, wantAction: importlist.SyncActionRemoveAndDelete},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			decisions, err := importlist.ApplySyncLevel(tt.level, current, existing)
			require.NoError(t, err)
			if tt.wantEmpty {
				assert.Empty(t, decisions)
				return
			}
			require.Len(t, decisions, 1)
			assert.Equal(t, "Fell Off", decisions[0].Item.Title)
			assert.Equal(t, tt.wantAction, decisions[0].Action)
		})
	}
}

func TestApplySyncLevelUnknownLevel(t *testing.T) {
	_, err := importlist.ApplySyncLevel(importlist.SyncLevel("bogus"), nil, nil)
	assert.ErrorIs(t, err, importlist.ErrUnknownSyncLevel)
}

func TestApplySyncLevelPartialOverlap(t *testing.T) {
	a := importlist.Item{Title: "A", ExternalIDs: importlist.ExternalIDs{TMDB: "1"}}
	b := importlist.Item{Title: "B", ExternalIDs: importlist.ExternalIDs{TMDB: "2"}}
	c := importlist.Item{Title: "C", ExternalIDs: importlist.ExternalIDs{TMDB: "3"}}
	current := []importlist.Item{b, c}  // the remote list now has B and C
	existing := []importlist.Item{a, b} // the catalog previously had A and B

	decisions, err := importlist.ApplySyncLevel(importlist.SyncLevelLogOnly, current, existing)

	require.NoError(t, err)
	require.Len(t, decisions, 1)
	assert.Equal(t, "A", decisions[0].Item.Title) // B is still present: no decision. C is new, not existing: irrelevant here.
	assert.Equal(t, importlist.SyncActionLog, decisions[0].Action)
}
