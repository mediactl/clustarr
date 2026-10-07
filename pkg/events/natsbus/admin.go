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
	"sort"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
)

var _ events.StreamAdmin = (*Bus)(nil)

// lookupStream resolves stream and remaps jetstream.ErrStreamNotFound to
// events.ErrStreamNotFound, as lookupError does for consumer lookups, so
// every StreamAdmin caller can errors.Is against the package sentinel
// instead of the jetstream one.
func (b *Bus) lookupStream(ctx context.Context, stream string) (jetstream.Stream, error) {
	st, err := b.js.Stream(ctx, stream)
	if err != nil {
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			return nil, fmt.Errorf("natsbus: stream %s: %w", stream, events.ErrStreamNotFound)
		}
		return nil, fmt.Errorf("natsbus: stream %s: %w", stream, err)
	}
	return st, nil
}

var _ events.StreamStater = (*Bus)(nil)

// StreamFill implements events.StreamStater: one STREAM.INFO.
func (b *Bus) StreamFill(ctx context.Context, stream string) (events.StreamFill, error) {
	s, err := b.lookupStream(ctx, stream)
	if err != nil {
		return events.StreamFill{}, err
	}
	info := s.CachedInfo()
	return events.StreamFill{
		Bytes:    info.State.Bytes,
		MaxBytes: uint64(max(info.Config.MaxBytes, 0)),
		Messages: info.State.Msgs,
	}, nil
}

// DeleteSubscription implements events.StreamAdmin.
func (b *Bus) DeleteSubscription(ctx context.Context, stream, durable string) error {
	for _, c := range [][2]string{{stream, durable}, {events.StreamAdvisories, events.DeadLetterWatcherName(stream, durable)}} {
		err := b.js.DeleteConsumer(ctx, c[0], c[1])
		if err != nil && !errors.Is(err, jetstream.ErrConsumerNotFound) && !errors.Is(err, jetstream.ErrStreamNotFound) {
			return fmt.Errorf("natsbus: delete consumer %s/%s: %w", c[0], c[1], err)
		}
	}
	return nil
}

// PurgeSubject implements events.StreamAdmin.
func (b *Bus) PurgeSubject(ctx context.Context, stream, subject string) error {
	st, err := b.lookupStream(ctx, stream)
	if err != nil {
		return err
	}
	if err := st.Purge(ctx, jetstream.WithPurgeSubject(subject)); err != nil {
		return fmt.Errorf("natsbus: purge %s on %s: %w", subject, stream, err)
	}
	return nil
}

// Subscriptions implements events.StreamAdmin.
func (b *Bus) Subscriptions(ctx context.Context, stream string) ([]string, error) {
	st, err := b.lookupStream(ctx, stream)
	if err != nil {
		return nil, err
	}
	names := st.ConsumerNames(ctx)
	var out []string
	for name := range names.Name() {
		out = append(out, name)
	}
	if err := names.Err(); err != nil {
		return nil, fmt.Errorf("natsbus: list consumers of %s: %w", stream, err)
	}
	sort.Strings(out)
	return out, nil
}

// ConsumerState implements events.StreamAdmin: one CONSUMER.INFO request
// (nats.go jetstream/consumer.go fetchConsumerInfo), read from the handle's
// cached info.
func (b *Bus) ConsumerState(ctx context.Context, stream, durable string) (events.ConsumerState, error) {
	c, err := b.js.Consumer(ctx, stream, durable)
	if err != nil {
		return events.ConsumerState{}, lookupError(stream, durable, err)
	}
	st, err := stateOf(c.CachedInfo(), b.nc.ConnectedClusterName() != "")
	if err != nil {
		return events.ConsumerState{}, fmt.Errorf("natsbus: consumer %s on %s: %w", durable, stream, err)
	}
	return st, nil
}

// stateOf reads a CONSUMER.INFO answer, which only the consumer's leader
// gives (nats-server jetstream_api.go:5556, 5649), so it is right on an R3
// cluster where /jsz on a follower reads NumPending 0 (split §9.0 as amended
// 2026-10-07). On a cluster, an answer with no placement is the "assigned, no
// Raft node yet" answer a member gives with zero state
// (jetstream_api.go:5675-5688): events.ErrConsumerUnavailable, never lag 0.
// A message past MaxDeliver waiting for its dead-letter copy counts in
// neither NumPending nor NumAckPending (consumer.go:2427-2453).
func stateOf(info *jetstream.ConsumerInfo, clustered bool) (events.ConsumerState, error) {
	if info == nil || clustered && info.Cluster == nil {
		return events.ConsumerState{}, events.ErrConsumerUnavailable
	}
	return events.ConsumerState{
		Pending:       info.NumPending,
		AckPending:    uint64(max(info.NumAckPending, 0)),
		Waiting:       info.NumWaiting,
		MaxAckPending: info.Config.MaxAckPending,
		ObservedAt:    info.TimeStamp,
	}, nil
}

// lookupError maps a consumer lookup's failure onto the events sentinels. A
// missing stream comes back from CONSUMER.INFO as an *APIError with
// JSErrCodeStreamNotFound, which is matched by code as well as by errors.Is.
func lookupError(stream, durable string, err error) error {
	var apiErr *jetstream.APIError
	switch {
	case errors.Is(err, jetstream.ErrStreamNotFound),
		errors.As(err, &apiErr) && apiErr.ErrorCode == jetstream.JSErrCodeStreamNotFound:
		return fmt.Errorf("natsbus: stream %s: %w", stream, events.ErrStreamNotFound)
	case errors.Is(err, jetstream.ErrConsumerNotFound):
		return fmt.Errorf("natsbus: consumer %s on %s: %w", durable, stream, events.ErrConsumerNotFound)
	}
	return fmt.Errorf("natsbus: consumer %s on %s: %w", durable, stream, err)
}

// Subjects implements events.StreamAdmin.
func (b *Bus) Subjects(ctx context.Context, stream, filter string) ([]string, error) {
	st, err := b.lookupStream(ctx, stream)
	if err != nil {
		return nil, err
	}
	info, err := st.Info(ctx, jetstream.WithSubjectFilter(filter))
	if err != nil {
		return nil, fmt.Errorf("natsbus: stream %s subjects %s: %w", stream, filter, err)
	}
	out := make([]string, 0, len(info.State.Subjects))
	for s := range info.State.Subjects {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

// Message implements events.StreamAdmin: one STREAM.MSG.GET by sequence
// (jetstream.Stream.GetMsg), its headers rebuilt into an envelope.
func (b *Bus) Message(ctx context.Context, stream string, seq uint64) (string, *events.Envelope, error) {
	st, err := b.lookupStream(ctx, stream)
	if err != nil {
		return "", nil, err
	}
	raw, err := st.GetMsg(ctx, seq)
	if err != nil {
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			return "", nil, fmt.Errorf("natsbus: %s seq %d: %w", stream, seq, events.ErrMessageNotFound)
		}
		return "", nil, fmt.Errorf("natsbus: %s seq %d: %w", stream, seq, err)
	}
	h := make(map[string]string, len(raw.Header))
	for k := range raw.Header {
		h[k] = raw.Header.Get(k)
	}
	return raw.Subject, events.EnvelopeFromHeaders(h, raw.Data), nil
}

// Missing implements events.StreamAdmin. Each object is looked up afresh by
// name: the bus's bound KV and object-store handles are not consulted, because
// a NATS restart can delete a memory-backed bucket under a live handle.
func (b *Bus) Missing(ctx context.Context, t events.Topology) ([]string, error) {
	var out []string
	for _, o := range t.Objects() {
		var err error
		switch o.Kind {
		case events.TopologyStream:
			_, err = b.js.Stream(ctx, o.Name)
		case events.TopologyConsumer:
			_, err = b.js.Consumer(ctx, o.Stream, o.Name)
		case events.TopologyBucket:
			_, err = b.js.KeyValue(ctx, o.Name)
		case events.TopologyObjectStore:
			_, err = b.js.ObjectStore(ctx, o.Name)
		}
		switch {
		case err == nil:
		case errors.Is(err, jetstream.ErrStreamNotFound),
			errors.Is(err, jetstream.ErrConsumerNotFound),
			errors.Is(err, jetstream.ErrBucketNotFound):
			out = append(out, o.String())
		default:
			return nil, fmt.Errorf("natsbus: look up %s: %w", o, err)
		}
	}
	return out, nil
}
