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

package events

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// EnsureTopology applies t to a live JetStream connection, creating what is
// missing and updating what has drifted. It is idempotent, so every replica
// of every service may call it at startup.
//
// It refuses to touch a stream whose retention policy differs from the
// topology, returning ErrRetentionImmutable: JetStream cannot change
// retention in place, and recreating a work stream would drop queued tasks.
// Migrating such a stream is an operator action.
func EnsureTopology(ctx context.Context, js jetstream.JetStream, t Topology) error {
	_, err := EnsureTopologyBlocked(ctx, js, t)
	return err
}

// EnsureTopologyBlocked is EnsureTopology, also returning, as
// TopologyObject.String spells each, what it left uncreated because a
// displaced pair's Old stream still exists (Topology.Displaced,
// unbranded-names spec §4.2.1). It never deletes a stream.
func EnsureTopologyBlocked(ctx context.Context, js jetstream.JetStream, t Topology) ([]string, error) {
	if err := t.Validate(); err != nil {
		return nil, fmt.Errorf("events: invalid topology: %w", err)
	}
	present := map[string]bool{}
	for _, d := range t.Displaced {
		_, err := js.Stream(ctx, d.Old)
		switch {
		case err == nil:
			present[d.Old] = true
		case errors.Is(err, jetstream.ErrStreamNotFound):
		default:
			return nil, fmt.Errorf("events: look up displaced stream %s: %w", d.Old, err)
		}
	}
	blockedSet := t.BlockedByDisplacement(func(old string) bool { return present[old] })
	var blocked []string
	for _, o := range t.Objects() {
		if blockedSet[o.String()] {
			blocked = append(blocked, o.String())
		}
	}
	for _, s := range t.Streams {
		if blockedSet[TopologyObject{Kind: TopologyStream, Name: s.Name}.String()] {
			continue
		}
		if err := ensureStream(ctx, js, s); err != nil {
			return blocked, err
		}
	}
	for _, c := range t.Consumers {
		if blockedSet[TopologyObject{Kind: TopologyConsumer, Stream: c.Stream, Name: c.Name}.String()] {
			continue
		}
		if err := EnsureConsumerJS(ctx, js, c); err != nil {
			return blocked, err
		}
	}
	for _, b := range t.Buckets {
		if _, err := js.CreateOrUpdateKeyValue(ctx, KeyValueConfig(b)); err != nil {
			return blocked, fmt.Errorf("events: ensure bucket %s: %w", b.Name, err)
		}
	}
	for _, o := range t.ObjectStores {
		if _, err := js.CreateOrUpdateObjectStore(ctx, ObjectStoreConfig(o)); err != nil {
			return blocked, fmt.Errorf("events: ensure object store %s: %w", o.Name, err)
		}
	}
	return blocked, nil
}

// EnsureConsumerJS creates or updates the durable c describes on c.Stream,
// handing it over from c.HandoverFrom when set (HandoverConsumerConfig).
func EnsureConsumerJS(ctx context.Context, js jetstream.JetStream, c ConsumerSpec) error {
	cfg, err := HandoverConsumerConfig(ctx, js, c)
	if err != nil {
		return err
	}
	if _, err := js.CreateOrUpdateConsumer(ctx, c.Stream, cfg); err != nil {
		return fmt.Errorf("events: ensure consumer %s on %s: %w", c.Name, c.Stream, err)
	}
	return nil
}

// HandoverConsumerConfig is ConsumerConfig(c), adjusted for a durable that
// takes over from c.HandoverFrom (unbranded-names spec §4.2.3):
//
//   - absent, with the legacy durable present: delivered by start sequence
//     from the legacy durable's ack floor + 1;
//   - present, with the legacy floor ahead of its own (the old release ran
//     after this one): deleted here, and recreated at the legacy floor + 1;
//   - present otherwise: its live DeliverPolicy, OptStartSeq and
//     OptStartTime carried over, since JetStream refuses to change them.
//
// The legacy durable is only read. Without HandoverFrom it is
// ConsumerConfig(c).
func HandoverConsumerConfig(ctx context.Context, js jetstream.JetStream, c ConsumerSpec) (jetstream.ConsumerConfig, error) {
	cfg := ConsumerConfig(c)
	if c.HandoverFrom == "" {
		return cfg, nil
	}
	legacyFloor, legacyOK, err := ackFloor(ctx, js, c.Stream, c.HandoverFrom)
	if err != nil {
		return cfg, err
	}
	live, err := js.Consumer(ctx, c.Stream, c.Name)
	switch {
	case err == nil:
		info := live.CachedInfo()
		if legacyOK && legacyFloor > info.AckFloor.Stream {
			if err := js.DeleteConsumer(ctx, c.Stream, c.Name); err != nil && !errors.Is(err, jetstream.ErrConsumerNotFound) {
				return cfg, fmt.Errorf("events: recreate %s on %s behind %s's ack floor: %w", c.Name, c.Stream, c.HandoverFrom, err)
			}
			cfg.DeliverPolicy, cfg.OptStartSeq = jetstream.DeliverByStartSequencePolicy, legacyFloor+1
			return cfg, nil
		}
		cfg.DeliverPolicy = info.Config.DeliverPolicy
		cfg.OptStartSeq = info.Config.OptStartSeq
		cfg.OptStartTime = info.Config.OptStartTime
	case errors.Is(err, jetstream.ErrConsumerNotFound):
		if legacyOK {
			cfg.DeliverPolicy, cfg.OptStartSeq = jetstream.DeliverByStartSequencePolicy, legacyFloor+1
		}
	case errors.Is(err, jetstream.ErrStreamNotFound):
	default:
		return cfg, fmt.Errorf("events: look up %s on %s: %w", c.Name, c.Stream, err)
	}
	return cfg, nil
}

// ackFloor reads durable's AckFloor.Stream on stream; ok is false when the
// durable or its stream does not exist.
func ackFloor(ctx context.Context, js jetstream.JetStream, stream, durable string) (floor uint64, ok bool, err error) {
	c, err := js.Consumer(ctx, stream, durable)
	switch {
	case err == nil:
		return c.CachedInfo().AckFloor.Stream, true, nil
	case errors.Is(err, jetstream.ErrConsumerNotFound), errors.Is(err, jetstream.ErrStreamNotFound):
		return 0, false, nil
	default:
		return 0, false, fmt.Errorf("events: read %s's ack floor on %s: %w", durable, stream, err)
	}
}

func ensureStream(ctx context.Context, js jetstream.JetStream, s StreamSpec) error {
	cfg := StreamConfig(s)
	existing, err := js.Stream(ctx, s.Name)
	switch {
	case err == nil:
		if got := existing.CachedInfo().Config.Retention; got != cfg.Retention {
			return fmt.Errorf("events: stream %s has retention %q, topology wants %q: %w",
				s.Name, got, cfg.Retention, ErrRetentionImmutable)
		}
	case errors.Is(err, jetstream.ErrStreamNotFound):
		// Nothing to compare; fall through to create.
	default:
		return fmt.Errorf("events: look up stream %s: %w", s.Name, err)
	}
	if _, err := js.CreateOrUpdateStream(ctx, cfg); err != nil {
		return fmt.Errorf("events: ensure stream %s: %w", s.Name, err)
	}
	return nil
}

// StreamConfig renders a StreamSpec as a JetStream stream configuration.
func StreamConfig(s StreamSpec) jetstream.StreamConfig {
	cfg := jetstream.StreamConfig{
		Name:              s.Name,
		Description:       s.Description,
		Subjects:          append([]string(nil), s.Subjects...),
		Retention:         natsRetention(s.Retention),
		Storage:           natsStorage(s.Storage),
		Discard:           natsDiscard(s.Discard),
		MaxAge:            s.MaxAge,
		MaxBytes:          s.MaxBytes,
		Duplicates:        s.Duplicates,
		Replicas:          max(s.Replicas, 1),
		DenyDelete:        s.DenyDelete,
		AllowDirect:       true,
		AllowMsgSchedules: s.AllowMsgSchedules,
	}
	if s.MaxBytes == 0 {
		cfg.MaxBytes = -1
	}
	if s.Compression {
		cfg.Compression = jetstream.S2Compression
	}
	return cfg
}

// ConsumerConfig renders a ConsumerSpec as a JetStream consumer
// configuration. Every Clustarr consumer is a durable explicit-ack pull
// consumer.
func ConsumerConfig(c ConsumerSpec) jetstream.ConsumerConfig {
	cfg := jetstream.ConsumerConfig{
		Name:          c.Name,
		Durable:       c.Name,
		Description:   c.Description,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckWait:       c.AckWait,
		MaxDeliver:    c.MaxDeliver,
		BackOff:       append([]time.Duration(nil), c.BackOff...),
		MaxAckPending: c.MaxAckPending,
		// ADR-0019 §8.2: the sampled ack metric feeds the manager's ack
		// delay histogram; "" samples nothing.
		SampleFrequency: c.SampleFrequency,
	}
	switch len(c.Filters) {
	case 0:
	case 1:
		cfg.FilterSubject = c.Filters[0]
	default:
		cfg.FilterSubjects = append([]string(nil), c.Filters...)
	}
	return cfg
}

// KeyValueConfig renders a BucketSpec as a JetStream key/value configuration.
func KeyValueConfig(b BucketSpec) jetstream.KeyValueConfig {
	return jetstream.KeyValueConfig{
		Bucket:         b.Name,
		Description:    b.Description,
		TTL:            b.TTL,
		History:        max(b.History, 1),
		Storage:        natsStorage(b.Storage),
		Replicas:       max(b.Replicas, 1),
		LimitMarkerTTL: b.LimitMarkerTTL,
		// nats.go maps 0 to unlimited (jetstream/kv.go).
		MaxBytes:     b.MaxBytes,
		MaxValueSize: b.MaxValueSize,
	}
}

// ObjectStoreConfig renders an ObjectStoreSpec as a JetStream object-store
// configuration, spec §B.1.
func ObjectStoreConfig(o ObjectStoreSpec) jetstream.ObjectStoreConfig {
	return jetstream.ObjectStoreConfig{
		Bucket:      o.Name,
		Description: o.Description,
		Storage:     natsStorage(o.Storage),
		MaxBytes:    o.MaxBytes,
		Replicas:    max(o.Replicas, 1),
		TTL:         o.MaxAge,
		Metadata:    maps.Clone(o.Metadata),
	}
}

func natsRetention(r Retention) jetstream.RetentionPolicy {
	switch r {
	case RetentionWorkQueue:
		return jetstream.WorkQueuePolicy
	case RetentionInterest:
		return jetstream.InterestPolicy
	default:
		return jetstream.LimitsPolicy
	}
}

func natsStorage(s Storage) jetstream.StorageType {
	if s == StorageMemory {
		return jetstream.MemoryStorage
	}
	return jetstream.FileStorage
}

func natsDiscard(d DiscardPolicy) jetstream.DiscardPolicy {
	if d == DiscardNew {
		return jetstream.DiscardNew
	}
	return jetstream.DiscardOld
}
