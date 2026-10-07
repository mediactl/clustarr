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
	"context"
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"

	"github.com/mediactl/clustarr/pkg/events"
)

var _ events.CoreSubscriber = (*Bus)(nil)

// SubscribeCore implements events.CoreSubscriber: a plain core NATS
// subscription (nc.Subscribe), no stream and no durable, ended by the stop
// function or by ctx. h runs on the subscription's delivery goroutine.
func (b *Bus) SubscribeCore(ctx context.Context, subject string, h func(subject string, data []byte)) (func(), error) {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return nil, events.ErrClosed
	}
	sub, err := b.nc.Subscribe(subject, func(m *nats.Msg) { h(m.Subject, m.Data) })
	if err != nil {
		return nil, fmt.Errorf("natsbus: subscribe %s: %w", subject, err)
	}
	var once sync.Once
	stop := func() { once.Do(func() { _ = sub.Unsubscribe() }) }
	go func() {
		<-ctx.Done()
		stop()
	}()
	return stop, nil
}
