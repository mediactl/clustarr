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
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mediactl/clustarr/pkg/events"
)

// A reply in parts.
//
// One NATS message holds no more than the server's max_payload, 8 MiB here,
// and a reply was one message: index's answer to rpc.index.download carries
// the .nzb, whose size grows with the release, and no compression helps past
// a point because an .nzb is mostly message-ids, which are random. A 153 GB
// season pack's gzipped to 6.14 MB against a budget of 6.03 (talos,
// 2026-11-27), the engine asked again, each asking was another download to
// the indexer, and the indexer then refused the release for a day.
//
// So a reply too large for one message crosses in several. The responder
// answers with the first part and three headers -- how many parts, how many
// bytes in all, and a subject of its own -- and then answers a request for
// each further part on that subject, by number. The requester asks for them
// in order and returns the whole. Nothing is pushed, so a part is never on
// the wire before its reader asked for it, and neither side buffers more
// than the reply itself.
//
// Both ends of an RPC must know this: a requester that does not would read
// the first part as the whole reply. That only ever reaches replies that
// did not cross at all before.
const (
	// headerReplyParts is how many parts the reply has, on its first.
	headerReplyParts = "Clustarr-Reply-Parts"
	// headerReplyBytes is the whole reply's length, on its first part.
	headerReplyBytes = "Clustarr-Reply-Bytes"
	// headerReplyMore is the subject that answers for parts 2 and on.
	headerReplyMore = "Clustarr-Reply-More"
	// headerReplyPart is the part a request on that subject asks for.
	headerReplyPart = "Clustarr-Reply-Part"
)

// replyHeadroom is what a message's headers and framing may take of
// max_payload beside its body.
const replyHeadroom = 4 << 10

// partsFallbackTimeout bounds serving a reply's parts when the handler's
// context carries no deadline.
const partsFallbackTimeout = 2 * time.Minute

// replyPartSize is how much of a reply one message carries: half the
// connection's max_payload, so a part is never near the limit whatever the
// headers weigh.
func (b *Bus) replyPartSize() int {
	limit := b.nc.MaxPayload()
	if limit <= 2*replyHeadroom {
		return replyHeadroom
	}
	return int(limit / 2)
}

// fitsOneMessage reports whether a reply of n bytes crosses as one message.
func (b *Bus) fitsOneMessage(n int) bool {
	return int64(n)+replyHeadroom <= b.nc.MaxPayload()
}

// respondInParts answers m with data in parts and serves the further parts
// until each has been asked for or ctx ends: the caller's own deadline, so a
// requester that went away costs its handler slot no longer than it would
// have waited.
func (b *Bus) respondInParts(ctx context.Context, m *nats.Msg, data []byte) error {
	if len(data) > events.MaxReplyBytes {
		return fmt.Errorf("%d bytes is over the %d a reply may span: %w", len(data), events.MaxReplyBytes, nats.ErrMaxPayload)
	}
	size := b.replyPartSize()
	parts := (len(data) + size - 1) / size
	part := func(i int) []byte { // 1-based
		return data[(i-1)*size : min(i*size, len(data))]
	}

	more := b.nc.NewInbox()
	sub, err := b.nc.SubscribeSync(more)
	if err != nil {
		return fmt.Errorf("subscribe for the reply's parts: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	first := &nats.Msg{Subject: m.Reply, Data: part(1), Header: nats.Header{}}
	first.Header.Set(headerReplyParts, strconv.Itoa(parts))
	first.Header.Set(headerReplyBytes, strconv.Itoa(len(data)))
	first.Header.Set(headerReplyMore, more)
	if err := m.RespondMsg(first); err != nil {
		return err
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, partsFallbackTimeout)
		defer cancel()
	}
	served := make([]bool, parts+1)
	served[1] = true
	for left := parts - 1; left > 0; {
		req, err := sub.NextMsgWithContext(ctx)
		if err != nil {
			// The requester stopped asking. It has its own error for that;
			// there is nobody to tell.
			return nil //nolint:nilerr // see above
		}
		i, err := strconv.Atoi(req.Header.Get(headerReplyPart))
		if err != nil || i < 2 || i > parts {
			respondError(req, events.ServiceErrorFailed,
				fmt.Sprintf("natsbus: the reply has no part %q of %d", req.Header.Get(headerReplyPart), parts))
			continue
		}
		if err := req.Respond(part(i)); err != nil {
			respondError(req, events.ServiceErrorFailed, events.OneLine(
				fmt.Errorf("natsbus: part %d of %d not sent: %w", i, parts, err)))
			continue
		}
		if !served[i] {
			served[i] = true
			left--
		}
	}
	return nil
}

// errReplyParts is a reply in parts that does not add up.
var errReplyParts = errors.New("natsbus: malformed reply in parts")

// collectParts returns the whole of a reply whose first part is first, or
// first's own data when the reply is one message.
func (b *Bus) collectParts(ctx context.Context, first *nats.Msg) ([]byte, error) {
	if first.Header.Get(headerReplyParts) == "" {
		return first.Data, nil
	}
	parts, perr := strconv.Atoi(first.Header.Get(headerReplyParts))
	total, terr := strconv.Atoi(first.Header.Get(headerReplyBytes))
	more := first.Header.Get(headerReplyMore)
	switch {
	case perr != nil || terr != nil || more == "" || parts < 2:
		return nil, fmt.Errorf("%w: parts %q, bytes %q", errReplyParts,
			first.Header.Get(headerReplyParts), first.Header.Get(headerReplyBytes))
	case total > events.MaxReplyBytes:
		return nil, fmt.Errorf("%w: %d bytes is over the %d a reply may span", errReplyParts, total, events.MaxReplyBytes)
	case len(first.Data) == 0 || len(first.Data) > total || parts > total:
		return nil, fmt.Errorf("%w: a first part of %d bytes in %d parts of %d", errReplyParts, len(first.Data), parts, total)
	}

	out := make([]byte, 0, total)
	out = append(out, first.Data...)
	for i := 2; i <= parts; i++ {
		req := &nats.Msg{Subject: more, Header: nats.Header{}}
		req.Header.Set(headerReplyPart, strconv.Itoa(i))
		reply, err := b.nc.RequestMsgWithContext(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("part %d of %d: %w", i, parts, err)
		}
		if msg := reply.Header.Get(headerServiceError); msg != "" {
			code, _ := strconv.Atoi(reply.Header.Get(headerServiceErrorCode))
			return nil, fmt.Errorf("part %d of %d: %w", i, parts, &events.ResponderError{Code: code, Message: msg})
		}
		if len(reply.Data) == 0 || len(out)+len(reply.Data) > total {
			return nil, fmt.Errorf("%w: part %d of %d carries %d bytes with %d of %d read",
				errReplyParts, i, parts, len(reply.Data), len(out), total)
		}
		out = append(out, reply.Data...)
	}
	if len(out) != total {
		return nil, fmt.Errorf("%w: %d bytes in %d parts, %d promised", errReplyParts, len(out), parts, total)
	}
	return out, nil
}
