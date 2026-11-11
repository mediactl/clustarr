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

package rescan

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// An identification call waits its turn at the metadata agent's MusicBrainz
// limiter for as long as the queue ahead of it takes, past the resolve
// timeout, with the walk heartbeating while it waits (review finding: at
// the 10 s resolve timeout rate.Limiter.Wait refused at once every folder
// asked while ten requests were queued, and the folder was listed
// lookup_failed until a person rescanned). The fake search waits on a real
// rate.Limiter under the caller's deadline, as the gateway's handler does.
func TestAnIdentificationWaitsItsTurnAtTheLimiterHeartbeating(t *testing.T) {
	old := identifyBeat
	identifyBeat = 10 * time.Millisecond
	t.Cleanup(func() { identifyBeat = old })

	limiter := rate.NewLimiter(rate.Every(300*time.Millisecond), 1)
	require.True(t, limiter.Allow(), "the queue ahead holds the next 300 ms")
	var beats atomic.Int64
	st := &scanState{keepAlive: func(context.Context, bool) error { beats.Add(1); return nil }}
	w := &Worker{
		MetadataTimeout: 100 * time.Millisecond, // the resolve timeout: shorter than the wait
		Search: func(ctx context.Context, _ commonv1.MediaKind, text, _ string) ([]metadata.SearchHit, error) {
			if err := limiter.Wait(ctx); err != nil {
				return nil, err
			}
			return []metadata.SearchHit{{Title: text}}, nil
		},
	}
	g := &gateway{w: w, st: st}

	hits, err := g.search(context.Background(), commonv1.MediaKindArtist, "Portishead", "")
	require.NoError(t, err, "the call waits its turn under the identification budget, not the resolve timeout")
	assert.Equal(t, []metadata.SearchHit{{Title: "Portishead"}}, hits)
	assert.NoError(t, g.err)
	assert.GreaterOrEqual(t, beats.Load(), int64(5), "the walk heartbeats while the call waits, not only before it")
}

// A heartbeat that fails while a call is in flight ends the call and the
// walk: the error is the heartbeat's, kept in g.err, never a lookup failure.
func TestAFailedHeartbeatEndsAnIdentificationCall(t *testing.T) {
	old := identifyBeat
	identifyBeat = 10 * time.Millisecond
	t.Cleanup(func() { identifyBeat = old })

	var beats atomic.Int64
	lost := context.Canceled
	st := &scanState{keepAlive: func(context.Context, bool) error {
		if beats.Add(1) > 2 {
			return lost
		}
		return nil
	}}
	w := &Worker{IdentifyTimeout: 5 * time.Second, Lookup: func(ctx context.Context, _ commonv1.MediaKind, _ map[string]string) (string, map[string]string, error) {
		<-ctx.Done() // a call that would wait out its whole budget
		return "", nil, ctx.Err()
	}}
	g := &gateway{w: w, st: st}
	_, _, err := g.lookup(context.Background(), commonv1.MediaKindArtist, map[string]string{metadata.KeyMBArtist: "x"})
	require.ErrorIs(t, err, lost)
	assert.ErrorIs(t, g.err, lost)
}
