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
	if err := t.Validate(); err != nil {
		return fmt.Errorf("events: invalid topology: %w", err)
	}
	for _, s := range t.Streams {
		if err := ensureStream(ctx, js, s); err != nil {
			return err
		}
	}
	for _, c := range t.Consumers {
		if _, err := js.CreateOrUpdateConsumer(ctx, c.Stream, ConsumerConfig(c)); err != nil {
			return fmt.Errorf("events: ensure consumer %s on %s: %w", c.Name, c.Stream, err)
		}
	}
	for _, b := range t.Buckets {
		if _, err := js.CreateOrUpdateKeyValue(ctx, KeyValueConfig(b)); err != nil {
			return fmt.Errorf("events: ensure bucket %s: %w", b.Name, err)
		}
	}
	for _, o := range t.ObjectStores {
		if _, err := js.CreateOrUpdateObjectStore(ctx, ObjectStoreConfig(o)); err != nil {
			return fmt.Errorf("events: ensure object store %s: %w", o.Name, err)
		}
	}
	return nil
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
