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

package natsbus_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/contracttest"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// lateAnswers is a TCP relay in front of a NATS server that, from the first
// time the client sends marker once armed, holds everything the server answers for
// delay: a stream that stores what it is sent and is slow to say so, which
// is what a stalled stream is to its publishers.
type lateAnswers struct {
	ln     net.Listener
	marker []byte
	delay  time.Duration

	mu    sync.Mutex
	armed bool
	until time.Time
	seen  bool
}

// arm has the next marker start the hold: a stream's own configuration
// names its subjects too, so the relay waits until the topology is in place.
func (l *lateAnswers) arm() {
	l.mu.Lock()
	l.armed = true
	l.mu.Unlock()
}

func newLateAnswers(t *testing.T, server string, marker string, delay time.Duration) *lateAnswers {
	t.Helper()
	u, err := url.Parse(server)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	l := &lateAnswers{ln: ln, marker: []byte(marker), delay: delay}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			upstream, err := net.Dial("tcp", u.Host)
			if err != nil {
				_ = client.Close()
				continue
			}
			go l.toServer(client, upstream)
			go l.toClient(upstream, client)
		}
	}()
	return l
}

func (l *lateAnswers) url() string { return "nats://" + l.ln.Addr().String() }

func (l *lateAnswers) toServer(client, upstream net.Conn) {
	defer func() { _ = upstream.Close() }()
	buf := make([]byte, 64<<10)
	var tail []byte // the end of the last read, for a marker that straddles two
	for {
		n, err := client.Read(buf)
		if n > 0 {
			window := append(append([]byte(nil), tail...), buf[:n]...)
			if bytes.Contains(window, l.marker) {
				l.mu.Lock()
				if l.armed && !l.seen {
					l.seen, l.until = true, time.Now().Add(l.delay)
				}
				l.mu.Unlock()
			}
			tail = append([]byte(nil), window[max(0, len(window)-len(l.marker)+1):]...)
			if _, werr := upstream.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (l *lateAnswers) toClient(upstream, client net.Conn) {
	defer func() { _ = client.Close() }()
	buf := make([]byte, 64<<10)
	for {
		n, err := upstream.Read(buf)
		if n > 0 {
			l.mu.Lock()
			wait := time.Until(l.until)
			l.mu.Unlock()
			if wait > 0 {
				time.Sleep(wait)
			}
			if _, werr := client.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// A Put whose acknowledgements take longer than nats.go's 5 s still
// succeeds. On talos the artwork stream stalls 6 to 10 s several times a
// day (nats-server reads what was written to the bucket since an object's
// last write, each time it is written again), and every Put caught in one
// failed with "nats: timeout waiting for ack" though its record and chunks
// were stored. The server here stores at once and its answers arrive 6 s
// late.
func TestAPutWaitsOutAStreamSlowToAcknowledge(t *testing.T) {
	const late = 6 * time.Second
	srv := startServer(t)
	relay := newLateAnswers(t, srv.ClientURL(), "$O."+events.BucketArtwork+".C.", late)
	nc, err := nats.Connect(relay.url())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	setup, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, bus.Ensure(setup, contracttest.Topology()))
	store := bus.ObjectStore(events.BucketArtwork)

	relay.arm()
	// As a work handler calls it: a context with no deadline of its own.
	body := bytes.Repeat([]byte{9}, 32<<10)
	start := time.Now()
	info, err := store.Put(context.Background(), "series/slow/poster/overlay", bytes.NewReader(body),
		events.ObjectMeta{Headers: map[string]string{events.HeaderContentType: "image/jpeg"}})
	require.NoError(t, err, "the stream stored it; only its answer was late")
	assert.GreaterOrEqual(t, time.Since(start), late-time.Second, "the answers were held")
	assert.EqualValues(t, len(body), info.Size)

	_, rc, err := store.Get(setup, "series/slow/poster/overlay")
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.Equal(t, body, got)
}
