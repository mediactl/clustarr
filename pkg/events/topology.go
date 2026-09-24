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
	"errors"
	"fmt"
	"time"
)

// Retention is a stream's message retention policy.
type Retention string

// Retention policies.
const (
	// RetentionLimits keeps messages until MaxAge or MaxBytes is reached.
	RetentionLimits Retention = "limits"

	// RetentionInterest keeps a message until every known consumer has
	// acknowledged it.
	RetentionInterest Retention = "interest"

	// RetentionWorkQueue removes a message as soon as one worker
	// acknowledges it. It is immutable once a stream exists.
	RetentionWorkQueue Retention = "workqueue"
)

// Storage is a stream's backing store.
type Storage string

// Storage kinds.
const (
	StorageFile   Storage = "file"
	StorageMemory Storage = "memory"
)

// DiscardPolicy is what a stream does when it hits a limit.
type DiscardPolicy string

// Discard policies.
const (
	// DiscardOld drops the oldest message to make room.
	DiscardOld DiscardPolicy = "old"

	// DiscardNew rejects the publish with ErrQueueFull rather than dropping
	// anything already stored. It cannot be combined with
	// StreamSpec.AllowMsgSchedules: nats-server rejects that pairing.
	DiscardNew DiscardPolicy = "new"
)

// Byte-size helpers for stream limits.
const (
	MiB = 1 << 20
	GiB = 1 << 30
)

// StreamSpec is the declarative configuration of one stream.
type StreamSpec struct {
	Name              string
	Description       string
	Subjects          []string
	Retention         Retention
	Storage           Storage
	Discard           DiscardPolicy
	MaxAge            time.Duration
	MaxBytes          int64
	Duplicates        time.Duration
	Replicas          int
	DenyDelete        bool
	Compression       bool
	AllowMsgSchedules bool
}

// ConsumerSpec is the declarative configuration of one durable pull consumer.
type ConsumerSpec struct {
	Name          string
	Stream        string
	Description   string
	Filters       []string
	AckWait       time.Duration
	MaxDeliver    int
	BackOff       []time.Duration
	MaxAckPending int
	Heartbeat     time.Duration
}

// Subscription converts the spec into the subscription a worker passes to
// Subscriber.Subscribe, so worker code never restates the tuning.
func (c ConsumerSpec) Subscription() Subscription {
	return Subscription{
		Stream:      c.Stream,
		Durable:     c.Name,
		Filters:     append([]string(nil), c.Filters...),
		AckWait:     c.AckWait,
		MaxDeliver:  c.MaxDeliver,
		Backoff:     append([]time.Duration(nil), c.BackOff...),
		MaxInFlight: c.MaxAckPending,
		Heartbeat:   c.Heartbeat,
	}
}

// TranscodeTaskConsumer is one pool's durable. It is not in Default(): pools
// come and go with profiles, so the worker's Pull creates it and squasharr's
// StreamAdmin deletes it. There is no Heartbeat: the worker sends InProgress
// itself while it renews its lease (spec §17.3). AckWait governs redelivery
// of a crashed worker's task; every other redelivery is an explicit Nak (a
// held lease's HeldRetry, a drain, a fence's Nak(0) left to lapse instead),
// whose delay is the worker's own choice (spec §18.1), so this carries no
// BackOff schedule -- a Subscription's Backoff replaces AckWait as the
// redelivery timer for every delivery (Subscription.Backoff's doc comment,
// JetStream semantics mirrored by membus), which would silently floor or
// override those explicit Nak delays instead of honouring them. Retry policy
// is squasharr's, not the queue's (spec §18.3); MaxDeliver is headroom for
// the worker's own settlement paths (held, drain, fence can each redeliver
// more than once) before the queue's own safety net -- dead-lettering a
// hung handler -- ever needs to fire.
func TranscodeTaskConsumer(profileUID, class string) ConsumerSpec {
	return ConsumerSpec{
		Name:          TranscodeTaskConsumerName(profileUID, class),
		Stream:        StreamWorkSquasharr,
		Description:   "One transcode pool's tasks.",
		Filters:       []string{FilterTranscodeTasks(profileUID, class)},
		AckWait:       60 * time.Second,
		MaxDeliver:    16,
		MaxAckPending: 64,
	}
}

// BucketSpec is the declarative configuration of one key/value bucket.
type BucketSpec struct {
	Name        string
	Description string
	TTL         time.Duration
	History     uint8
	Storage     Storage
	Replicas    int

	// LimitMarkerTTL is how long tombstones for TTL-expired keys are kept.
	// A non-zero value is required for per-key TTL (KV WithTTL) to work.
	LimitMarkerTTL time.Duration
}

// Topology is the full broker layout Clustarr expects: every stream, durable
// consumer and key/value bucket.
type Topology struct {
	Streams   []StreamSpec
	Consumers []ConsumerSpec
	Buckets   []BucketSpec
}

// Stream returns the named stream spec.
func (t Topology) Stream(name string) (StreamSpec, bool) {
	for _, s := range t.Streams {
		if s.Name == name {
			return s, true
		}
	}
	return StreamSpec{}, false
}

// Consumer returns the named consumer spec.
func (t Topology) Consumer(name string) (ConsumerSpec, bool) {
	for _, c := range t.Consumers {
		if c.Name == name {
			return c, true
		}
	}
	return ConsumerSpec{}, false
}

// StreamForSubject returns the stream whose subject filters cover subject.
func (t Topology) StreamForSubject(subject string) (StreamSpec, bool) {
	for _, s := range t.Streams {
		for _, f := range s.Subjects {
			if SubjectMatches(f, subject) {
				return s, true
			}
		}
	}
	return StreamSpec{}, false
}

// singleNodeMemoryBudget is the total MaxBytes ForSingleNode will reserve
// across every stream once it has switched them to memory storage.
//
// It exists because switching storage without resizing is not a safe
// translation. The production topology reserves roughly 8 GiB across its
// streams, which is unremarkable on disk and impossible in memory: the
// single-node NATS in config/nats sets max_memory_store to 256Mi on a pod
// with a 1Gi limit, so the reservations are rejected and every controller
// CrashLoopBackOffs on the first stream it tries to create. 64 MiB leaves
// room for the buckets and for NATS' own overhead inside that ceiling.
const singleNodeMemoryBudget = 64 * MiB

// ForSingleNode returns a copy of t with one replica per stream and bucket
// and memory storage throughout. It is what tests and single-node dev
// clusters apply; production applies Default unchanged.
//
// Stream MaxBytes is scaled to fit singleNodeMemoryBudget, keeping the
// relative sizing the production topology chose rather than flattening every
// stream to the same cap.
func (t Topology) ForSingleNode() Topology {
	out := t.clone()

	for i := range out.Streams {
		out.Streams[i].Replicas = 1
		out.Streams[i].Storage = StorageMemory
		out.Streams[i].Compression = false
		out.Streams[i].DenyDelete = false
	}
	scaleToBudget(out.Streams, singleNodeMemoryBudget, singleNodeMinStreamBytes)
	for i := range out.Buckets {
		out.Buckets[i].Replicas = 1
		out.Buckets[i].Storage = StorageMemory
	}
	return out
}

// singleNodeMinStreamBytes keeps a proportionally tiny stream usable. A
// stream whose MaxBytes rounds to near zero rejects its first publish, which
// reads as a broken bus rather than a full one.
const singleNodeMinStreamBytes = 1 * MiB

// scaleToBudget shrinks the streams' MaxBytes in proportion until together
// they reserve no more than budget, giving any stream whose share would fall
// below floor the floor instead. The floors come out of the budget first, so
// raising a tiny stream to its floor cannot push the total over the budget,
// as scaling everything and then flooring did once the advisory stream
// (64 MiB of ~9 GiB) was added. A stream with no MaxBytes is unlimited and is
// left alone.
func scaleToBudget(streams []StreamSpec, budget, floor int64) {
	var total int64
	for _, s := range streams {
		total += max(s.MaxBytes, 0)
	}
	if total <= budget {
		return
	}
	floored := map[int]bool{}
	for {
		var rest int64
		for i, s := range streams {
			if s.MaxBytes > 0 && !floored[i] {
				rest += s.MaxBytes
			}
		}
		left := budget - int64(len(floored))*floor
		grew := false
		for i, s := range streams {
			if s.MaxBytes > 0 && !floored[i] && s.MaxBytes*left/rest < floor {
				floored[i] = true
				grew = true
			}
		}
		if grew {
			continue
		}
		for i, s := range streams {
			switch {
			case s.MaxBytes <= 0:
			case floored[i]:
				streams[i].MaxBytes = floor
			default:
				streams[i].MaxBytes = s.MaxBytes * left / rest
			}
		}
		return
	}
}

func (t Topology) clone() Topology {
	out := Topology{
		Streams:   append([]StreamSpec(nil), t.Streams...),
		Consumers: append([]ConsumerSpec(nil), t.Consumers...),
		Buckets:   append([]BucketSpec(nil), t.Buckets...),
	}
	for i := range out.Streams {
		out.Streams[i].Subjects = append([]string(nil), out.Streams[i].Subjects...)
	}
	for i := range out.Consumers {
		out.Consumers[i].Filters = append([]string(nil), out.Consumers[i].Filters...)
		out.Consumers[i].BackOff = append([]time.Duration(nil), out.Consumers[i].BackOff...)
	}
	return out
}

// Validate checks the invariants the runtime depends on:
//
//   - every consumer names a stream in the topology;
//   - every consumer filter is covered by that stream's subjects;
//   - MaxDeliver is strictly greater than len(BackOff), so the last attempt
//     is a real attempt and not an unused backoff step;
//   - every WorkQueue work stream allows message schedules, since delay
//     profiles publish grabs into the future, and no stream pairs schedules
//     with DiscardNew, which nats-server refuses;
//   - the dead-letter stream and the advisory stream exist.
func (t Topology) Validate() error {
	var errs []error
	seen := map[string]StreamSpec{}
	for _, s := range t.Streams {
		if s.Name == "" {
			errs = append(errs, fieldErr("StreamSpec.Name", "is required"))
			continue
		}
		if _, dup := seen[s.Name]; dup {
			errs = append(errs, fieldErr("StreamSpec.Name", "duplicate stream "+s.Name))
		}
		seen[s.Name] = s
		if len(s.Subjects) == 0 {
			errs = append(errs, fieldErr(s.Name+".Subjects", "is required"))
		}
		// The advisory stream and the transcode stream are WorkQueues, but only
		// JetStream publishes to the advisory, and transcode uses DiscardNew so
		// it has no schedules.
		if s.Retention == RetentionWorkQueue && !s.AllowMsgSchedules &&
			s.Name != StreamAdvisories && s.Name != StreamWorkSquasharr {
			errs = append(errs, fieldErr(s.Name+".AllowMsgSchedules",
				"work streams must allow message schedules, so delay profiles "+
					"can publish a grab into the future"))
		}
		if s.AllowMsgSchedules && s.Discard == DiscardNew {
			errs = append(errs, fieldErr(s.Name+".Discard",
				"nats-server refuses DiscardNew on a stream that allows message "+
					"schedules; use DiscardOld and size MaxBytes for headroom"))
		}
	}
	for _, required := range []string{StreamDLQ, StreamAdvisories} {
		if _, ok := seen[required]; !ok {
			errs = append(errs, fieldErr("Topology.Streams",
				"the "+required+" stream is required"))
		}
	}
	names := map[string]bool{}
	for _, c := range t.Consumers {
		if c.Name == "" {
			errs = append(errs, fieldErr("ConsumerSpec.Name", "is required"))
			continue
		}
		if names[c.Name] {
			errs = append(errs, fieldErr("ConsumerSpec.Name", "duplicate consumer "+c.Name))
		}
		names[c.Name] = true
		s, ok := seen[c.Stream]
		if !ok {
			errs = append(errs, fieldErr(c.Name+".Stream",
				"unknown stream "+c.Stream))
			continue
		}
		if c.MaxDeliver <= len(c.BackOff) {
			errs = append(errs, fieldErr(c.Name+".MaxDeliver",
				fmt.Sprintf("must be strictly greater than len(BackOff)=%d, got %d",
					len(c.BackOff), c.MaxDeliver)))
		}
		if len(c.Filters) == 0 {
			errs = append(errs, fieldErr(c.Name+".Filters", "is required"))
		}
		for _, f := range c.Filters {
			if !subjectCovered(s.Subjects, f) {
				errs = append(errs, fieldErr(c.Name+".Filters",
					"filter "+f+" is outside stream "+s.Name))
			}
		}
	}
	buckets := map[string]bool{}
	for _, b := range t.Buckets {
		if b.Name == "" {
			errs = append(errs, fieldErr("BucketSpec.Name", "is required"))
			continue
		}
		if buckets[b.Name] {
			errs = append(errs, fieldErr("BucketSpec.Name", "duplicate bucket "+b.Name))
		}
		buckets[b.Name] = true
		for _, r := range b.Name {
			if r == '.' {
				errs = append(errs, fieldErr("BucketSpec.Name",
					"bucket names cannot contain dots: "+b.Name))
				break
			}
		}
	}
	return errors.Join(errs...)
}

// subjectCovered reports whether filter is inside one of the stream subjects.
// A stream subject of "a.b.>" covers "a.b.c.>" and "a.b.c.d".
func subjectCovered(subjects []string, filter string) bool {
	probe := filter
	if n := len(probe); n > 2 && probe[n-2:] == ".>" {
		probe = probe[:n-2] + ".x"
	}
	for _, s := range subjects {
		if s == filter || SubjectMatches(s, probe) {
			return true
		}
	}
	return false
}

func fieldErr(field, msg string) error {
	return fmt.Errorf("events: %s %s", field, msg)
}

// ConsumerCatalogRedownload is the durable consumer that turns a failed
// Download into a redownload search (spec §8.3): catalogarr frees the item's
// grab lease and publishes a catalog.SearchTask with reason redownload.
//
// It and its filters live here rather than beside the other names in
// subjects.go only because this file is the one the task that added it owned
// (gap fixes Y3); they are ordinary exported names of the package.
const ConsumerCatalogRedownload = "catalogarr-redownload"

// The two download-event subjects ConsumerCatalogRedownload filters.
//
// failed is the one spec §8.3 names. blocklisted is the other way a Download
// can end blamed on its release: grabarr derives Blocklisted from the
// download.clustarr.io/blocklisted label ahead of any telemetry, so a
// Download labelled while it is still transferring goes straight to
// Blocklisted and never publishes failed -- and Radarr's "remove from queue
// and blocklist" is exactly a mark-as-failed, which redownloads. A Download
// that fails and is then blocklisted publishes both; the redownload search's
// deterministic Msg-Id (per Download UID) makes the pair one search.
const (
	FilterDownloadFailed      = "clustarr.evt.download.download.failed.>"
	FilterDownloadBlocklisted = "clustarr.evt.download.download.blocklisted.>"
)

// Default returns the production topology from the Clustarr design: eight
// streams, fourteen durable consumers and ten key/value buckets.
//
// Three declarations the design once carried are gone because nothing ever
// used them (gap fixes Z2): the catalogarr-import consumer and its
// clustarr.work.catalogarr.import subject, which amendment §A1 replaced with
// importarr-fileimport; the indexarr-definitions consumer and its
// definitions-sync subject, which no worker consumed and nothing published,
// because Cardigann definitions arrive through indexarr's startup bundle
// loader; and the clustarr-search-cache bucket, which nothing read or wrote.
// Ensure never deletes, so a broker that already holds the two consumers or
// the bucket keeps them, idle and empty, until an operator removes them.
func Default() Topology {
	return Topology{
		Streams:   defaultStreams(),
		Consumers: defaultConsumers(),
		Buckets:   defaultBuckets(),
	}
}

func defaultStreams() []StreamSpec {
	// Work streams keep WorkQueue retention, an hour of deduplication and
	// message schedules, which delay profiles need to publish a grab into
	// the future.
	//
	// The design asks for DiscardNew as well, so a full queue refuses new
	// tasks instead of dropping queued ones. nats-server rejects that
	// pairing outright ("message scheduling cannot use discard new"), so
	// these streams use DiscardOld and are sized with enough headroom that
	// the limit is an alarm, not a routine event. Publishers still handle
	// ErrQueueFull, which remains reachable on any stream an operator
	// configures with DiscardNew.
	work := func(name, filter string, maxBytes int64) StreamSpec {
		return StreamSpec{
			Name:              name,
			Subjects:          []string{filter},
			Retention:         RetentionWorkQueue,
			Storage:           StorageFile,
			Discard:           DiscardOld,
			MaxBytes:          maxBytes,
			Duplicates:        time.Hour,
			Replicas:          3,
			AllowMsgSchedules: true,
		}
	}
	return []StreamSpec{
		{
			Name:        StreamEvents,
			Description: "Domain events consumed by the history projector.",
			Subjects:    []string{FilterAllEvents},
			Retention:   RetentionLimits,
			Storage:     StorageFile,
			Discard:     DiscardOld,
			MaxAge:      168 * time.Hour,
			MaxBytes:    2 * GiB,
			Duplicates:  10 * time.Minute,
			Replicas:    3,
			DenyDelete:  true,
			Compression: true,
		},
		{
			Name:        StreamReleases,
			Description: "Parsed indexer releases fanned out to the RSS matcher.",
			Subjects:    []string{FilterAllReleases},
			Retention:   RetentionLimits,
			Storage:     StorageFile,
			Discard:     DiscardOld,
			MaxAge:      72 * time.Hour,
			MaxBytes:    4 * GiB,
			Duplicates:  2 * time.Hour,
			Replicas:    3,
		},
		work(StreamWorkCatalogarr, FilterWorkCatalogarr, 1*GiB),
		// importarr (amendment §A1.6). Sized above indexarr's and
		// captionarr's because a first scan of a large library enqueues one
		// message per directory chunk and every completed download enqueues
		// a fileimport; the other two enqueue per indexer and per subtitle
		// request.
		work(StreamWorkImportarr, FilterWorkImportarr, 512*MiB),
		work(StreamWorkIndexarr, FilterWorkIndexarr, 256*MiB),
		work(StreamWorkCaptionarr, FilterWorkCaptionarr, 256*MiB),
		{
			Name:        StreamWorkSquasharr,
			Description: "Transcode tasks squasharr admitted, and the workers' status events.",
			Subjects:    []string{FilterWorkSquasharr},
			Retention:   RetentionWorkQueue,
			Storage:     StorageFile,
			Discard:     DiscardNew,
			MaxBytes:    64 * MiB,
			Duplicates:  time.Hour,
			Replicas:    3,
		},
		{
			Name:        StreamDLQ,
			Description: "Dead-lettered tasks awaiting operator replay.",
			Subjects:    []string{FilterAllDLQ},
			Retention:   RetentionLimits,
			Storage:     StorageFile,
			Discard:     DiscardOld,
			MaxAge:      720 * time.Hour,
			MaxBytes:    1 * GiB,
			Duplicates:  10 * time.Minute,
			Replicas:    3,
		},
		{
			// Gap fixes Z2. WorkQueue, so an advisory is gone once a
			// replica has dead-lettered its message and acknowledged it,
			// and a watcher consumer created later is not handed advisories
			// already dealt with. It outlives the DLQ's own retention:
			// a work-queue message JetStream gave up on stays in its stream
			// until it is copied, however long every replica is down. An
			// advisory is under a kilobyte.
			Name:        StreamAdvisories,
			Description: "MAX_DELIVERIES advisories awaiting a dead-letter copy.",
			Subjects:    []string{FilterMaxDeliveriesAdvisories},
			Retention:   RetentionWorkQueue,
			Storage:     StorageFile,
			Discard:     DiscardOld,
			MaxAge:      720 * time.Hour,
			MaxBytes:    64 * MiB,
			Replicas:    3,
		},
	}
}

func defaultConsumers() []ConsumerSpec {
	s := time.Second
	m := time.Minute
	h := time.Hour
	return []ConsumerSpec{
		{
			Name: ConsumerCatalogRSSMatcher, Stream: StreamReleases,
			Filters: []string{FilterAllReleases},
			AckWait: 30 * s, MaxDeliver: 6,
			BackOff:       []time.Duration{1 * s, 5 * s, 30 * s, 2 * m, 10 * m},
			MaxAckPending: 256,
		},
		{
			Name: ConsumerCatalogSearchHigh, Stream: StreamWorkCatalogarr,
			Filters: []string{"clustarr.work.catalogarr.search.high.>"},
			AckWait: 120 * s, MaxDeliver: 5,
			BackOff:       []time.Duration{30 * s, 2 * m, 10 * m},
			MaxAckPending: 8,
		},
		{
			Name: ConsumerCatalogSearchNorm, Stream: StreamWorkCatalogarr,
			Filters: []string{
				"clustarr.work.catalogarr.search.normal.>",
				"clustarr.work.catalogarr.search.low.>",
				FilterCatalogWanted,
			},
			AckWait: 120 * s, MaxDeliver: 5,
			BackOff:       []time.Duration{30 * s, 2 * m, 10 * m, 1 * h},
			MaxAckPending: 8,
		},
		{
			Name: ConsumerCatalogGrab, Stream: StreamWorkCatalogarr,
			Filters: []string{FilterCatalogGrab},
			AckWait: 60 * s, MaxDeliver: 5,
			BackOff:       []time.Duration{10 * s, 1 * m, 5 * m},
			MaxAckPending: 16,
		},
		{
			Name: ConsumerCatalogMetadata, Stream: StreamWorkCatalogarr,
			Filters: []string{FilterCatalogMetadata},
			AckWait: 60 * s, MaxDeliver: 8,
			BackOff:       []time.Duration{30 * s, 2 * m, 10 * m, 1 * h, 6 * h},
			MaxAckPending: 32,
		},
		{
			Name: ConsumerCatalogHistory, Stream: StreamEvents,
			Filters: []string{FilterAllEvents},
			AckWait: 30 * s, MaxDeliver: 3,
			BackOff:       []time.Duration{5 * s, 30 * s},
			MaxAckPending: 512,
		},
		{
			// Gap fixes Y3 (spec §8.3). On EVENTS beside catalogarr-history,
			// which reads the same subjects as history; a second durable
			// is what gives this one its own acks and redeliveries. The
			// work is a lease delete and a publish per item, so 30s is
			// ample; MaxDeliver 6 leaves room for the handler's short
			// wait for grabarr's blocklist label (two 5s retries) on top
			// of the four backoff steps for real failures.
			Name: ConsumerCatalogRedownload, Stream: StreamEvents,
			Filters: []string{FilterDownloadFailed, FilterDownloadBlocklisted},
			AckWait: 30 * s, MaxDeliver: 6,
			BackOff:       []time.Duration{5 * s, 30 * s, 2 * m, 10 * m},
			MaxAckPending: 16,
		},
		// importarr (amendment §A1.6). AckWait is 60s on all three, which is
		// the floor set by terminationGracePeriodSeconds: 60 in
		// config/manager/importarr-worker.yaml. A worker that is SIGTERMed
		// must be able to finish or give up an in-flight message inside the
		// grace period, or the pod is killed mid-task and the message is
		// only redelivered after AckWait expires. Any unit of work that can
		// outlast 60s -- a chunked directory walk, a large hardlink-or-copy
		// import -- must send in-progress acks rather than have its AckWait
		// raised past the grace period, and the manifest and this value must
		// be changed together.
		{
			Name: ConsumerImportScan, Stream: StreamWorkImportarr,
			Filters: []string{FilterImportScan},
			AckWait: 60 * s, MaxDeliver: 4,
			BackOff:       []time.Duration{30 * s, 2 * m, 10 * m},
			MaxAckPending: 4, Heartbeat: 30 * s,
		},
		{
			Name: ConsumerImportList, Stream: StreamWorkImportarr,
			Filters: []string{FilterImportList},
			AckWait: 60 * s, MaxDeliver: 4,
			BackOff:       []time.Duration{5 * m, 30 * m, 2 * h},
			MaxAckPending: 2, Heartbeat: 30 * s,
		},
		{
			Name: ConsumerImportFile, Stream: StreamWorkImportarr,
			Filters: []string{FilterImportFile},
			AckWait: 60 * s, MaxDeliver: 5,
			BackOff:       []time.Duration{30 * s, 2 * m, 10 * m, 1 * h},
			MaxAckPending: 4, Heartbeat: 30 * s,
		},
		{
			// AckWait is 60s, the floor set by indexarr's
			// terminationGracePeriodSeconds: 60 (config/manager/indexarr.yaml).
			// It was 120s, which broke the rule the importarr block above
			// states: a worker that is SIGTERMed must be able to finish or
			// give up an in-flight message inside the grace period, or the
			// pod is killed mid-task and the message is only redelivered
			// after AckWait expires. An RSS poll of a slow indexer can
			// outlast 60s, so the worker sends in-progress acks on this
			// heartbeat rather than having AckWait raised past the grace
			// period. Spec 5's consumer table carries the same 60s.
			Name: ConsumerIndexRSS, Stream: StreamWorkIndexarr,
			Filters: []string{FilterIndexRSS},
			AckWait: 60 * s, MaxDeliver: 4,
			BackOff:       []time.Duration{1 * m, 5 * m, 15 * m},
			MaxAckPending: 4,
			Heartbeat:     30 * s,
		},
		{
			Name: ConsumerCaptionFetchHigh, Stream: StreamWorkCaptionarr,
			Filters: []string{"clustarr.work.captionarr.fetch.high.>"},
			AckWait: 90 * s, MaxDeliver: 8,
			BackOff:       []time.Duration{30 * s, 2 * m, 10 * m, 1 * h, 6 * h},
			MaxAckPending: 16,
		},
		{
			Name: ConsumerCaptionFetchNormal, Stream: StreamWorkCaptionarr,
			Filters: []string{
				"clustarr.work.captionarr.fetch.normal.>",
				"clustarr.work.captionarr.fetch.low.>",
			},
			AckWait: 90 * s, MaxDeliver: 8,
			BackOff:       []time.Duration{30 * s, 2 * m, 10 * m, 1 * h, 6 * h},
			MaxAckPending: 16,
		},
		{
			Name: ConsumerSquasharrResults, Stream: StreamWorkSquasharr,
			Description: "Worker status events: squasharr sets TranscodeJob status and decides the next step.",
			Filters:     []string{FilterTranscodeResults},
			AckWait:     30 * s, MaxDeliver: 10,
			BackOff:       []time.Duration{5 * s, 30 * s, 2 * m},
			MaxAckPending: 1,
		},
		{
			Name: ConsumerDLQProjector, Stream: StreamDLQ,
			Filters: []string{FilterAllDLQ},
			AckWait: 30 * s, MaxDeliver: 3,
			BackOff:       []time.Duration{5 * s, 30 * s},
			MaxAckPending: 64,
		},
	}
}

func defaultBuckets() []BucketSpec {
	const marker = 5 * time.Minute
	b := func(name string, ttl time.Duration, desc string) BucketSpec {
		return BucketSpec{
			Name: name, TTL: ttl, Description: desc,
			History: 1, Storage: StorageFile, Replicas: 3,
			LimitMarkerTTL: marker,
		}
	}
	return []BucketSpec{
		b(BucketLeases, 0, "Double-grab guard; keys are created, never put."),
		b(BucketPending, 7*24*time.Hour, "Best pending candidate per media key."),
		b(BucketImportExclusions, 0, "Exclusions the list and search paths consult; durable."),
		b(BucketIndexerSessions, 30*24*time.Hour, "Cardigann cookies and JWTs."),
		b(BucketIndexerLimits, 2*24*time.Hour, "Query and grab timestamp rings."),
		b(BucketProviderThrottle, 24*time.Hour, "Subtitle provider throttle table."),
		b(BucketMetadataCache, 30*24*time.Hour, "L2 metadata cache."),
		b(BucketProgress, 10*time.Minute, "1 Hz download and transcode telemetry."),
		b(BucketTranscodeLeases, TranscodeLeaseTTL,
			"Transcode task leases: created by the claiming worker, renewed with Update, expired by the server; squasharr writes cancel markers."),
		b(BucketImportList, 7*24*time.Hour, "Import list items, kept out of status."),
		b(BucketDedup, 24*time.Hour, "Import fingerprints for re-import no-ops."),
	}
}
