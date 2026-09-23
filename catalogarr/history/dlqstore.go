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

package history

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
)

// ErrDeadLetterNotFound is returned by a [DLQReader] when CLUSTARR_DLQ holds
// no message at the sequence asked for: it aged out (the stream keeps 30
// days), was purged, or never existed.
var ErrDeadLetterNotFound = errors.New("history: no dead letter at that sequence")

// DLQReader reads dead letters back off CLUSTARR_DLQ by stream sequence --
// the one thing replaying one needs that events.Bus does not offer, since a
// work consumer never needs to address a message by position.
//
// It is an interface so the replay handler and the projector can be tested
// against a fake; [JetStreamDLQ] is the production implementation.
type DLQReader interface {
	// GetDeadLetter returns the dead letter stored at seq: the DLQ subject
	// it was stored on and its envelope, Clustarr-DLQ-* headers included.
	GetDeadLetter(ctx context.Context, seq uint64) (subject string, env *events.Envelope, err error)

	// LastDeadLetterSeq returns the sequence of the newest dead letter
	// stored on dlqSubject. The projector uses it to tell an operator which
	// sequence to replay: a dead letter's DLQ subject ends in its envelope
	// id, so the newest one on that subject is this message or a copy of
	// the very same message dead-lettered again.
	LastDeadLetterSeq(ctx context.Context, dlqSubject string) (uint64, error)
}

// JetStreamDLQ is the [DLQReader] over a JetStream context --
// natsbus.Bus.JetStream(), the same connection the bus itself uses.
type JetStreamDLQ struct {
	JS jetstream.JetStream
}

var _ DLQReader = JetStreamDLQ{}

// DLQReaderFor returns the [DLQReader] for bus when bus is backed by
// JetStream (natsbus.Bus), and false otherwise -- the in-memory bus keeps
// no stream to read back, so replay is unavailable there and the caller
// should not register it.
func DLQReaderFor(bus events.Bus) (DLQReader, bool) {
	js, ok := bus.(interface{ JetStream() jetstream.JetStream })
	if !ok || js.JetStream() == nil {
		return nil, false
	}
	return JetStreamDLQ{JS: js.JetStream()}, true
}

// GetDeadLetter implements [DLQReader].
func (d JetStreamDLQ) GetDeadLetter(ctx context.Context, seq uint64) (string, *events.Envelope, error) {
	s, err := d.JS.Stream(ctx, events.StreamDLQ)
	if err != nil {
		return "", nil, fmt.Errorf("history: bind %s: %w", events.StreamDLQ, err)
	}
	msg, err := s.GetMsg(ctx, seq)
	if err != nil {
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			return "", nil, fmt.Errorf("%w: %d", ErrDeadLetterNotFound, seq)
		}
		return "", nil, fmt.Errorf("history: get dead letter %d: %w", seq, err)
	}
	h := make(map[string]string, len(msg.Header))
	for k := range msg.Header {
		h[k] = msg.Header.Get(k)
	}
	return msg.Subject, events.EnvelopeFromHeaders(h, msg.Data), nil
}

// LastDeadLetterSeq implements [DLQReader].
func (d JetStreamDLQ) LastDeadLetterSeq(ctx context.Context, dlqSubject string) (uint64, error) {
	s, err := d.JS.Stream(ctx, events.StreamDLQ)
	if err != nil {
		return 0, fmt.Errorf("history: bind %s: %w", events.StreamDLQ, err)
	}
	msg, err := s.GetLastMsgForSubject(ctx, dlqSubject)
	if err != nil {
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			return 0, fmt.Errorf("%w: last on %s", ErrDeadLetterNotFound, dlqSubject)
		}
		return 0, fmt.Errorf("history: last dead letter on %s: %w", dlqSubject, err)
	}
	return msg.Sequence, nil
}
