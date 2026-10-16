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

package limits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
)

// A grab pause is the indexer's own refusal made local: a payload fetch it
// answered 429 pauses every grab on it until Retry-After, or without one for
// DefaultGrabPause doubling on each repeat up to MaxGrabPause, and a grab
// that succeeds ends the run. The grab ring counts grabs against
// spec.limits.grabLimit, which the indexer's allowance may not match; before
// this, a 429 gave its slot back, the ring read "under the configured
// limits", and three usenet grabs spent nzb-life's allowance as fast as it
// came back for seven hours (talos, 2026-10-16).
const (
	DefaultGrabPause = 15 * time.Minute
	MaxGrabPause     = 6 * time.Hour
)

// GrabPauseKey is the pause entry for the indexer with uid in
// clustarr-indexer-limits.
func GrabPauseKey(uid string) string { return events.KVKeyToken(uid) + ".grab-pause" }

type grabPause struct {
	// Until is when the pause ends, Unix seconds.
	Until int64 `json:"until"`
	// Strikes is how many refusals in a row set it.
	Strikes int32 `json:"strikes"`
}

// PauseGrabs records a refusal at now and returns when grabs may resume:
// now plus retryAfter when the indexer named one, else the escalating
// default.
func PauseGrabs(ctx context.Context, kv events.KV, idx *indexv1alpha1.Indexer, retryAfter time.Duration, now time.Time) (time.Time, error) {
	cur, _, err := readPause(ctx, kv, idx)
	if err != nil {
		return time.Time{}, err
	}
	strikes := cur.Strikes + 1
	pause := retryAfter
	if pause <= 0 {
		pause = DefaultGrabPause
		for i := int32(1); i < strikes && pause < MaxGrabPause; i++ {
			pause *= 2
		}
		pause = min(pause, MaxGrabPause)
	}
	until := now.Add(pause)
	val, err := json.Marshal(grabPause{Until: until.Unix(), Strikes: strikes})
	if err != nil {
		return time.Time{}, err
	}
	if _, err := kv.Put(ctx, GrabPauseKey(string(idx.UID)), val); err != nil {
		return time.Time{}, fmt.Errorf("app/indexer/limits: record the grab pause: %w", err)
	}
	return until, nil
}

// GrabPaused reports the pause in force at now, if any.
func GrabPaused(ctx context.Context, kv events.KV, idx *indexv1alpha1.Indexer, now time.Time) (time.Time, bool, error) {
	cur, have, err := readPause(ctx, kv, idx)
	if err != nil || !have {
		return time.Time{}, false, err
	}
	until := time.Unix(cur.Until, 0).UTC()
	if !until.After(now) {
		return time.Time{}, false, nil
	}
	return until, true, nil
}

// ClearGrabPause ends the run after a grab the indexer served.
func ClearGrabPause(ctx context.Context, kv events.KV, idx *indexv1alpha1.Indexer) error {
	err := kv.Delete(ctx, GrabPauseKey(string(idx.UID)))
	if err != nil && !errors.Is(err, events.ErrKeyNotFound) {
		return fmt.Errorf("app/indexer/limits: clear the grab pause: %w", err)
	}
	return nil
}

func readPause(ctx context.Context, kv events.KV, idx *indexv1alpha1.Indexer) (grabPause, bool, error) {
	e, err := kv.Get(ctx, GrabPauseKey(string(idx.UID)))
	if errors.Is(err, events.ErrKeyNotFound) {
		return grabPause{}, false, nil
	}
	if err != nil {
		return grabPause{}, false, fmt.Errorf("app/indexer/limits: read the grab pause: %w", err)
	}
	var p grabPause
	if len(e.Value) == 0 {
		return grabPause{}, false, nil
	}
	if err := json.Unmarshal(e.Value, &p); err != nil {
		return grabPause{}, false, fmt.Errorf("app/indexer/limits: decode the grab pause: %w", err)
	}
	return p, true, nil
}
