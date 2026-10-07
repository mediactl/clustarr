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

package subtitleprofile

import (
	"context"
	"sync/atomic"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/event"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
)

// WakeDebounce is how often the loop's signals reach the reconciler (loop
// spec §2.13).
const WakeDebounce = 10 * time.Second

// Wake is the loop's subtitleProfileWake (loop spec §2.13, §3.3 S1'): a
// file's winning SubtitleProfile or its subtitles phase changed. Signals
// inside one debounce make one event, which re-counts every profile. It is
// one of the two MediaFile-driven channels TestOnlyTheLoopWatchesMediaFile
// allows outside the loop.
type Wake struct {
	pending  atomic.Bool
	events   chan event.GenericEvent
	debounce time.Duration
}

// NewWake is a Wake that delivers at most one event per debounce.
func NewWake(debounce time.Duration) *Wake {
	return &Wake{events: make(chan event.GenericEvent, 1), debounce: debounce}
}

// Signal never blocks: the loop calls it from its event handler.
func (w *Wake) Signal() { w.pending.Store(true) }

// Channel is the reconciler's raw source.
func (w *Wake) Channel() <-chan event.GenericEvent { return w.events }

// NeedLeaderElection makes the wake leader-only, as the reconciler is.
func (w *Wake) NeedLeaderElection() bool { return true }

// Start turns pending signals into one event per debounce until ctx ends.
func (w *Wake) Start(ctx context.Context) error {
	t := time.NewTicker(w.debounce)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if w.pending.Swap(false) {
				select {
				case w.events <- event.GenericEvent{Object: &subtitlev1alpha1.SubtitleProfile{}}:
				default: // one already queued: it re-counts everything anyway
				}
			}
		}
	}
}
