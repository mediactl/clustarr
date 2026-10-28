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

package engine_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/engine"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// fromIndexer is the present command for entry n, its payload on indexer
// "tracker".
func fromIndexer(n int, seq int64) schema.EngineCommand {
	cmd := command(fmt.Sprintf("entry-%02d", n), fmt.Sprintf("uid-%02d", n), seq, schema.EngineDesiredPresent)
	cmd.Source = &commonv1.DownloadSource{IndexerDownload: &commonv1.IndexerDownload{IndexerRef: "tracker"}}
	return cmd
}

// appliedEntries is the entry of every apply, in order.
func appliedEntries(hs *harness) []string {
	hs.src.mu.Lock()
	defer hs.src.mu.Unlock()
	var out []string
	for _, c := range hs.src.calls {
		if strings.HasPrefix(c, "apply") {
			out = append(out, c)
		}
	}
	return out
}

// An indexer that defers its payloads as a whole -- its grab limit, or the
// pause a 429 set -- is asked once when the wait ends, not once per entry
// waiting on it: the first entry deferred is asked first, the next only once
// that one was served, and an entry that arrives while the indexer is
// paused waits behind them without asking at all. On talos (2026-10-28)
// 1,165 nzb-life grabs each kept a timer of their own, every one firing when
// the pause ended, and the index agent answered 15,403 refusals in 43
// minutes for 12 grabs.
func TestAnIndexerPausedForAllItsGrabsIsAskedOnceWhenThePauseEnds(t *testing.T) {
	restore := engine.SetDeferralFloor(10 * time.Millisecond)
	defer restore()
	ctx := context.Background()
	hs := newHarness(t)
	hs.start(t)

	var mu sync.Mutex
	until := time.Now().Add(80 * time.Millisecond)
	served := 0
	hs.src.mu.Lock()
	hs.src.calls = nil
	hs.src.applyHook = func(schema.EngineCommand) error {
		mu.Lock()
		defer mu.Unlock()
		if time.Now().Before(until) || served == 1 {
			// Paused; after the one grab it serves, paused for an hour.
			if served == 1 {
				until = time.Now().Add(time.Hour)
			}
			return engine.PayloadDeferred(until, errors.New("index: grab limit reached for media/tracker"))
		}
		served++
		return nil
	}
	hs.src.mu.Unlock()

	for n := range 20 {
		require.NoError(t, hs.h.Handle(ctx, delivery(t, fromIndexer(n, 10))))
	}
	assert.Equal(t, []string{"apply present seq=10 cur="}, appliedEntries(hs),
		"only the first entry asks; the rest wait behind the paused indexer")

	require.Eventually(t, func() bool { return len(appliedEntries(hs)) == 3 }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	assert.Len(t, appliedEntries(hs), 3,
		"when the pause ends the first entry is served and the second asked, which finds it paused again; no other entry asks")
	_, ok := hs.src.get("entry-00")
	assert.True(t, ok, "the entry deferred first is served first")
	_, ok = hs.src.get("entry-01")
	assert.False(t, ok, "the second waits for the next pause to end")
}

// A release's own cooldown (a 429 naming the release, ReleaseRefusals) holds
// that release alone: the indexer's other releases are still asked.
func TestAReleasesOwnCooldownDoesNotHoldItsIndexer(t *testing.T) {
	restore := engine.SetDeferralFloor(10 * time.Millisecond)
	defer restore()
	ctx := context.Background()
	hs := newHarness(t)
	hs.start(t)

	until := time.Now().Add(time.Hour)
	hs.src.mu.Lock()
	hs.src.calls = nil
	hs.src.applyHook = func(cmd schema.EngineCommand) error {
		if cmd.Entry.UID == "uid-00" {
			return engine.IndexerDeferred(schema.DownloadResponse{RetryAt: &until, ReleaseRefusals: 1},
				errors.New("index: grab limit reached for media/tracker"))
		}
		return nil
	}
	hs.src.mu.Unlock()

	require.NoError(t, hs.h.Handle(ctx, delivery(t, fromIndexer(0, 10))))
	require.NoError(t, hs.h.Handle(ctx, delivery(t, fromIndexer(1, 10))))
	assert.Len(t, appliedEntries(hs), 2, "the other release is asked for")
	_, ok := hs.src.get("entry-01")
	assert.True(t, ok)
}

// finding is memTransfers with a Finder: the handler finds a command's
// transfer through it and never lists.
type finding struct{ *memTransfers }

func (f finding) Find(_ context.Context, entryUID, name string) (*engine.Held, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("find %s", name)
	for i := range f.held {
		if f.held[i].Journal.EntryUID == entryUID || (f.held[i].Journal.EntryUID == "" && f.held[i].Name == name) {
			h := f.held[i]
			return &h, nil
		}
	}
	return nil, nil
}

// A Transfers that can find one entry's transfer is asked for that one: the
// handler lists no transfer for a command.
func TestACommandFindsItsTransferWithoutListingEvery(t *testing.T) {
	ctx := context.Background()
	hs := newHarness(t, journaled(entryID, entryUID, 3))
	hs.start(t)
	hs.h.Transfers = finding{hs.src}
	hs.src.mu.Lock()
	hs.src.calls = nil
	hs.src.mu.Unlock()

	require.NoError(t, hs.h.Handle(ctx, delivery(t, command(entryID, entryUID, 4, schema.EngineDesiredPresent))))
	assert.Equal(t, []string{"find " + entryID, "apply present seq=4 cur=" + entryID}, hs.src.Calls())
}
