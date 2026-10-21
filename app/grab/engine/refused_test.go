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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/engine"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// One refusal of a release defers its command until the cooldown ends; a
// release refused again after its cooldown is given up -- nzb-life refused
// three Naked and Afraid grabs every day for five days (2026-10-15..20).
func TestAReleaseRefusedTwiceIsGivenUp(t *testing.T) {
	at := time.Date(2026, 10, 21, 0, 41, 33, 0, time.UTC)
	once := engine.IndexerDeferred(schema.DownloadResponse{Error: "cooldown", RetryAt: &at, ReleaseRefusals: 1}, errors.New("cooldown"))
	var deferred *engine.PayloadDeferredError
	require.ErrorAs(t, once, &deferred)
	require.Equal(t, at, deferred.Until)
	require.NotErrorIs(t, once, engine.ErrReleaseRefused)

	twice := engine.IndexerDeferred(schema.DownloadResponse{Error: "cooldown", RetryAt: &at, ReleaseRefusals: 2}, errors.New("cooldown"))
	require.ErrorIs(t, twice, engine.ErrReleaseRefused)
	require.False(t, errors.As(twice, &deferred))

	other := engine.IndexerDeferred(schema.DownloadResponse{Error: "grab limit", RetryAt: &at}, errors.New("grab limit"))
	require.ErrorAs(t, other, &deferred, "an indexer-wide limit only defers")
}

// The engine reports a given-up release as failed releaseRefused, which
// the manager neither blocklists nor retries on the release.
func TestAGivenUpReleaseIsReportedReleaseRefused(t *testing.T) {
	hs := newHarness(t)
	hs.start(t)
	hs.src.applyErr = fmt.Errorf("%w: usenetengine: index: cooldown", engine.ErrReleaseRefused)
	require.NoError(t, hs.h.Handle(context.Background(), delivery(t, command(entryID, entryUID, 12, schema.EngineDesiredPresent))))
	rec, ok := hs.transfer(t, entryUID)
	require.True(t, ok)
	require.Equal(t, schema.TransferStateFailed, rec.State)
	require.Equal(t, commonv1.DownloadFailureReleaseRefused, rec.EngineFailureReason)
}
