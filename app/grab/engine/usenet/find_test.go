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

package usenet_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/grab/engine"
	usenetengine "github.com/mediactl/clustarr/app/grab/engine/usenet"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// listingClient counts List: Find must never list.
type listingClient struct {
	*journaledClient
	lists int
}

func (l *listingClient) List(ctx context.Context) ([]download.Item, error) {
	l.lists++
	return l.journaledClient.List(ctx)
}

// Find is the engine's find rule -- the transfer whose journal names the
// entry, else a pre-journal one under the entry's id -- read from the one
// job named for the entry, never by listing every job: on talos
// (2026-10-28) the list behind every command was a quarter of the engine's
// CPU.
func TestFindReadsTheJobNamedForTheEntryWithoutListing(t *testing.T) {
	ctx := context.Background()
	jc := &listingClient{journaledClient: &journaledClient{fakeDownloadClient: newFakeDownloadClient(), journals: map[string][]byte{}}}
	job := func(id, name, uid string) {
		jc.setItem(download.Item{ID: id, Name: name, Status: download.StatusQueued, AddedAt: time.Now()})
		if uid == "" {
			return
		}
		cmd := schema.EngineCommand{Seq: 7, Entry: schema.EntryRef{ID: name, UID: uid}, Desired: schema.EngineDesiredPresent}
		b, err := engine.MarshalJournal(engine.NextJournal(engine.Journal{}, cmd, time.Now()))
		require.NoError(t, err)
		require.NoError(t, jc.SetJournal(ctx, id, b))
	}
	job("job-a", "entry-a", "uid-a")
	job("job-p", "entry-p", "")
	job("job-x", "entry-x", "uid-old")
	tr := &usenetengine.Transfers{Client: jc}
	var _ engine.Finder = tr

	h, err := tr.Find(ctx, "uid-a", "entry-a")
	require.NoError(t, err)
	require.NotNil(t, h)
	assert.Equal(t, "job-a", h.Item.ID)
	assert.Equal(t, "uid-a", h.Journal.EntryUID)
	assert.Equal(t, int64(7), h.Journal.Seq)
	assert.True(t, h.Found)

	h, err = tr.Find(ctx, "uid-p", "entry-p")
	require.NoError(t, err)
	require.NotNil(t, h, "a pre-journal job is the entry's by name")
	assert.Equal(t, "job-p", h.Item.ID)
	assert.Empty(t, h.Journal.EntryUID)

	h, err = tr.Find(ctx, "uid-new", "entry-x")
	require.NoError(t, err)
	assert.Nil(t, h, "a job whose journal names another entry is not this one's")

	h, err = tr.Find(ctx, "uid-z", "entry-z")
	require.NoError(t, err)
	assert.Nil(t, h)

	assert.Zero(t, jc.lists, "Find never lists")
}
