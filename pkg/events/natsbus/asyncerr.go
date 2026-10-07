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

package natsbus

import (
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// asyncErrorEvery bounds the log to one line per kind and subject a minute;
// the counter sees every one.
const asyncErrorEvery = time.Minute

// asyncErrorKind is the bounded label an asynchronous error is counted under:
// slow_consumer, permission or other. A disconnect is counted by onDisconnect.
func asyncErrorKind(err error) string {
	switch {
	case errors.Is(err, nats.ErrSlowConsumer):
		return "slow_consumer"
	case strings.Contains(strings.ToLower(err.Error()), "permissions violation"):
		return "permission"
	default:
		return "other"
	}
}

// asyncErrors reports a connection's asynchronous errors: nats.go drops
// messages for a slow consumer and calls only this callback, which nothing
// installed before the NATS research of 2026-10-07 (S7).
type asyncErrors struct {
	log  *slog.Logger
	mu   sync.Mutex
	last map[string]time.Time // kind + "\x00" + subject
}

func (a *asyncErrors) onError(_ *nats.Conn, sub *nats.Subscription, err error) {
	if err == nil {
		return
	}
	kind := asyncErrorKind(err)
	metrics.NATSAsyncErrorsTotal.WithLabelValues(kind).Inc()
	subject := ""
	if sub != nil {
		subject = sub.Subject
	}
	if a.due(kind, subject) {
		a.log.Warn("nats: asynchronous error", "kind", kind, "subject", subject, "error", err)
	}
}

func (a *asyncErrors) onDisconnect(_ *nats.Conn, err error) {
	if err == nil {
		return // a clean close
	}
	metrics.NATSAsyncErrorsTotal.WithLabelValues("disconnect").Inc()
	if a.due("disconnect", "") {
		a.log.Warn("nats: disconnected", "error", err)
	}
}

func (a *asyncErrors) due(kind, subject string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	k := kind + "\x00" + subject
	if time.Since(a.last[k]) < asyncErrorEvery {
		return false
	}
	a.last[k] = time.Now()
	return true
}
