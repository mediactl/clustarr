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
	"time"
)

// Receipt is the broker's acknowledgement of a publish.
type Receipt struct {
	// Stream is the stream that stored the message.
	Stream string

	// Seq is the stream sequence the message was stored at. For a duplicate
	// it is the sequence of the message already stored.
	Seq uint64

	// Duplicate reports that the broker recognised HeaderMsgID inside the
	// stream's deduplication window and stored nothing new.
	Duplicate bool
}

// PublishOptions is the resolved effect of a list of PublishOption values.
// Bus implementations read it instead of the option funcs themselves.
type PublishOptions struct {
	// MsgID is the deduplication key. It defaults to Envelope.ID.
	MsgID string

	// ScheduleAt, when non-zero, holds the message until that instant.
	ScheduleAt time.Time

	// ExpectStream, when set, fails the publish unless the subject resolves
	// to that stream.
	ExpectStream string
}

// PublishOption modifies a single Publish call.
type PublishOption func(*PublishOptions)

// WithMsgID overrides Envelope.ID as the deduplication key for this publish.
func WithMsgID(id string) PublishOption {
	return func(o *PublishOptions) { o.MsgID = id }
}

// WithScheduleAt asks the broker to hold the message until t. It requires the
// target stream to have AllowMsgSchedules set, which every CLUSTARR_WORK_*
// stream does. Scheduling in the past delivers immediately.
func WithScheduleAt(t time.Time) PublishOption {
	return func(o *PublishOptions) { o.ScheduleAt = t }
}

// WithExpectStream fails the publish unless the subject resolves to the named
// stream. It guards against a subject typo silently landing in another
// stream.
func WithExpectStream(s string) PublishOption {
	return func(o *PublishOptions) { o.ExpectStream = s }
}

// ResolvePublishOptions folds opts over the defaults implied by e.
func ResolvePublishOptions(e *Envelope, opts []PublishOption) PublishOptions {
	var o PublishOptions
	if e != nil {
		o.MsgID = e.ID
	}
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// Publisher writes messages to a stream.
type Publisher interface {
	// Publish stores e on subject and waits for the broker acknowledgement.
	// It returns ErrQueueFull when a DiscardNew work stream is at its limit.
	Publish(ctx context.Context, subject string, e *Envelope, opts ...PublishOption) (Receipt, error)
}

// Subscription describes a durable pull consumer. The zero value is not
// usable: Stream, Durable and Filters are required.
type Subscription struct {
	// Stream is the stream to consume from.
	Stream string

	// Durable is the durable consumer name, shared by every replica of a
	// service so they compete for the same messages.
	Durable string

	// Filters limits delivery to these subjects. Wildcards are allowed.
	Filters []string

	// AckWait is how long the broker waits for a settlement before it
	// redelivers. It applies only when Backoff is empty: see Backoff.
	AckWait time.Duration

	// MaxDeliver caps total delivery attempts. It must be strictly greater
	// than len(Backoff) so the final attempt is not a fresh backoff step.
	MaxDeliver int

	// Backoff is the redelivery schedule. Entry i is the delay applied after
	// attempt i+1 fails; attempts past the end reuse the last entry.
	//
	// When it is set it is also the acknowledgement deadline, replacing
	// AckWait, because that is what JetStream does: delivery n that goes
	// unsettled -- a handler still running, not one that failed -- is
	// redelivered Backoff[n-1] after it was made (the last entry past the
	// end). Every bus follows that rule.
	Backoff []time.Duration

	// MaxInFlight caps unacknowledged messages held by this consumer.
	MaxInFlight int

	// Heartbeat asks the broker for idle heartbeats at this interval, so a
	// long-idle consumer notices a broken connection.
	Heartbeat time.Duration
}

// Validate reports whether the subscription is internally consistent.
func (s Subscription) Validate() error {
	switch {
	case s.Stream == "":
		return fieldErr("Subscription.Stream", "is required")
	case s.Durable == "":
		return fieldErr("Subscription.Durable", "is required")
	case len(s.Filters) == 0:
		return fieldErr("Subscription.Filters", "is required")
	case s.MaxDeliver > 0 && s.MaxDeliver <= len(s.Backoff):
		return fieldErr("Subscription.MaxDeliver",
			"must be strictly greater than len(Backoff)")
	}
	return nil
}

// Subscriber reads messages from a stream through a durable pull consumer.
type Subscriber interface {
	// Subscribe creates or updates the durable consumer described by s and
	// starts delivering to h. The returned stop function drains in-flight
	// handlers and detaches; it does not delete the durable consumer.
	Subscribe(ctx context.Context, s Subscription, h Handler) (stop func(), err error)
}

// Puller hands out one message per Next call from a durable pull consumer.
// Nothing is fetched ahead, so a caller that works on a message for hours
// never holds a second, prefetched one past its ack window. The caller
// settles each message itself (Ack, Nak, Term); nothing settles it for them.
type Puller interface {
	// Next blocks until the consumer delivers a message to this caller or
	// ctx ends. The returned context carries Hooks.AfterReceive's result.
	Next(ctx context.Context) (context.Context, Message, error)
	// Stop releases the puller. It never deletes the durable.
	Stop()
}

// PullSubscriber is a bus that can pull one message at a time. Pull creates
// or updates the durable s describes; s.MaxInFlight is the durable's
// MaxAckPending across every puller that shares it.
type PullSubscriber interface {
	Pull(ctx context.Context, s Subscription) (Puller, error)
}

// StreamAdmin removes queue state whose owner is gone.
type StreamAdmin interface {
	// DeleteSubscription deletes the durable and its dead-letter watcher.
	// A missing one is not an error.
	DeleteSubscription(ctx context.Context, stream, durable string) error
	// PurgeSubject removes every stored message on subject, which may be a
	// wildcard filter ("*" for one token, a trailing ">" for the rest) as
	// well as a literal subject -- every implementation matches it as
	// [SubjectMatches] does.
	PurgeSubject(ctx context.Context, stream, subject string) error
	// Subjects lists the subjects under filter that hold stored messages.
	Subjects(ctx context.Context, stream, filter string) ([]string, error)
}

// Requester is the micro-style request/reply half of the bus: a single reply
// per request, load-balanced across a queue group.
type Requester interface {
	// Request encodes in as JSON, sends it to subject and decodes the single
	// reply into out. It returns ErrNoResponders when nothing is serving the
	// subject and the context error on deadline.
	Request(ctx context.Context, subject string, in, out any) error

	// Serve registers h as a responder on subject inside queue group queue.
	// Handlers run until the bus is closed.
	Serve(subject, queue string, h func(ctx context.Context, data []byte) ([]byte, error)) error
}

// Entry is a single key/value revision.
type Entry struct {
	// Bucket is the bucket the entry was read from.
	Bucket string

	// Key is the entry key.
	Key string

	// Value is the stored bytes. It is nil for a delete or purge entry.
	Value []byte

	// Revision is the monotonic revision of this value, used for CAS updates.
	Revision uint64

	// Created is when this revision was written.
	Created time.Time

	// Delta is how far this entry is behind the bucket's latest revision.
	Delta uint64

	// Operation says whether this revision is a put, a delete or a purge.
	Operation KVOp
}

// KVOp is the kind of revision an Entry represents.
type KVOp string

// Key/value operations.
const (
	KVPut    KVOp = "put"
	KVDelete KVOp = "delete"
	KVPurge  KVOp = "purge"
)

// KVOptions is the resolved effect of a list of KVOption values.
type KVOptions struct {
	// TTL is the per-key expiry, overriding the bucket TTL when non-zero.
	TTL time.Duration
}

// KVOption modifies a single key/value write.
type KVOption func(*KVOptions)

// WithTTL gives the created key its own expiry, overriding the bucket TTL.
func WithTTL(d time.Duration) KVOption {
	return func(o *KVOptions) { o.TTL = d }
}

// ResolveKVOptions folds opts into a KVOptions value.
func ResolveKVOptions(opts []KVOption) KVOptions {
	var o KVOptions
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// KV is a single bucket of the broker's key/value store.
type KV interface {
	// Get returns the current revision of key, or ErrKeyNotFound.
	Get(ctx context.Context, key string) (Entry, error)

	// Create writes key only if it is absent, returning ErrKeyExists
	// otherwise. This is the primitive behind the grab lease.
	Create(ctx context.Context, key string, val []byte, opts ...KVOption) (uint64, error)

	// Update writes key only if its current revision is rev, returning
	// ErrRevisionMismatch otherwise. It also clears any per-key TTL.
	Update(ctx context.Context, key string, val []byte, rev uint64) (uint64, error)

	// Put writes key unconditionally.
	Put(ctx context.Context, key string, val []byte) (uint64, error)

	// Delete places a delete marker on key. Deleting an absent key is not an
	// error.
	Delete(ctx context.Context, key string) error

	// DeleteRevision places a delete marker on key only if its current
	// revision is rev, returning ErrRevisionMismatch otherwise -- including
	// when the key has been deleted or has expired since rev was read. It is
	// Delete with Update's compare-and-swap: a caller that read a value and
	// decided to delete it cannot delete a value another writer put there
	// meanwhile, as a value check followed by Delete can.
	DeleteRevision(ctx context.Context, key string, rev uint64) error

	// Watch streams the current value of every key matching pattern and then
	// every subsequent change. The channel is closed when ctx is cancelled or
	// the bus closes.
	Watch(ctx context.Context, pattern string) (<-chan Entry, error)
}

// Bus is the whole broker contract: publish, subscribe, request/reply,
// key/value and topology management.
type Bus interface {
	Publisher
	Subscriber
	Requester

	// KV binds to a bucket created by Ensure.
	KV(bucket string) KV

	// Ensure creates or updates every stream, consumer and bucket in t. It is
	// idempotent and safe to run from every replica at startup. It returns
	// ErrRetentionImmutable rather than recreating a stream whose retention
	// policy has changed.
	Ensure(ctx context.Context, t Topology) error

	// Close releases the connection and stops every subscription and
	// responder.
	Close() error
}

// Hooks are the observability hooks a bus implementation calls around every
// publish and receive. They are plain function fields rather than an
// interface onto a specific observability package because pkg/events must
// not import pkg/obs: the dependency points the other way. Each concrete Bus
// (natsbus.Bus, membus.Bus) accepts a Hooks value through a constructor
// option, matching the package's existing functional-option style; service
// wiring supplies the hooks, typically pkg/obs/tracing.Inject and Extract via
// obs.BusHooks().
//
// The zero value is the correct "no observability" default: both fields are
// nil-checked before use, so a bus stays usable in tests and anywhere else
// with no hooks installed.
type Hooks struct {
	// BeforePublish runs on every outbound envelope, before it is encoded
	// onto the wire, so a header it sets (e.g. HeaderTrace) is on the wire
	// copy. pkg/obs/tracing.Inject satisfies it as written.
	BeforePublish func(ctx context.Context, e *Envelope)

	// AfterReceive runs on every inbound envelope, before the handler, and
	// returns the context the handler is given. pkg/obs/tracing.Extract
	// satisfies it as written.
	AfterReceive func(ctx context.Context, e *Envelope) context.Context
}

// RunBeforePublish calls h.BeforePublish if it is set. It is a no-op on the
// zero Hooks, so bus implementations can call it unconditionally.
func (h Hooks) RunBeforePublish(ctx context.Context, e *Envelope) {
	if h.BeforePublish != nil {
		h.BeforePublish(ctx, e)
	}
}

// RunAfterReceive calls h.AfterReceive if it is set and returns its result.
// A nil hook, or a hook that itself returns nil, is treated as "ctx
// unchanged" rather than as a nil context -- so bus implementations can call
// it unconditionally and pass the result straight to the handler.
func (h Hooks) RunAfterReceive(ctx context.Context, e *Envelope) context.Context {
	if h.AfterReceive == nil {
		return ctx
	}
	if out := h.AfterReceive(ctx, e); out != nil {
		return out
	}
	return ctx
}
