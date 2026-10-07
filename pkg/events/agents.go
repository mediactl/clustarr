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
	"crypto/sha1" //nolint:gosec // a dedup key, not a security boundary, as MsgIDForRelease
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The NATS state ADR-0019 adds (design §4.3, §5.1, §6.7, §8.2): agents
// report through compare-and-swap records (records.go's agentRecordBuckets)
// and the intake stream, the manager commands engines through the engine
// stream, and JetStream's nak and term advisories for every task the manager
// dispatches land on the task-events stream. The manager creates every one
// of these objects; no agent does.
const (
	// StreamIntake carries proposals and observations the manager did not
	// request: candidates and scan observations. Durable (file-backed at
	// full size on a single node) and DiscardNew: a full intake refuses
	// and the publisher retries; nothing is evicted.
	StreamIntake = "CLUSTARR_INTAKE"
	// StreamWorkEngine carries the manager's commands to engine instances,
	// one durable per instance (EngineConsumer).
	StreamWorkEngine = "CLUSTARR_WORK_ENGINE"
	// StreamTaskEvents captures MSG_NAKED and MSG_TERMINATED advisories of
	// every Dispatched durable (TaskEventSubjects); only JetStream publishes
	// to it.
	StreamTaskEvents = "CLUSTARR_TASK_EVENTS"

	// FilterIntake is StreamIntake's subjects.
	FilterIntake = "clustarr.intake.>"
	// FilterIntakeCandidates and FilterIntakeScans are its two durables'.
	FilterIntakeCandidates = "clustarr.intake.candidate.>"
	FilterIntakeScans      = "clustarr.intake.scan.>"
	// FilterWorkEngine is StreamWorkEngine's subjects.
	FilterWorkEngine = "clustarr.work.engine.>"

	// ConsumerIntakeCandidate is the manager's leader-only candidate
	// inbox (app/intake).
	ConsumerIntakeCandidate = "catalogarr-intake-candidate"
	// ConsumerIntakeScan is the manager's leader-only scan applier
	// (app/import/manager/scanapply).
	ConsumerIntakeScan = "importarr-intake-scan"
	// ConsumerTaskEvents is the manager's leader-only advisory intake
	// (app/intake/advisory).
	ConsumerTaskEvents = "clustarr-task-events"

	// EngineConsumerPrefix begins every engine instance's durable.
	EngineConsumerPrefix = "grabarr-engine-"

	// RPCIndexBlocklist is the release index's blocklist verb (design
	// §6.14), beside search, download and query, in queue group indexarr.
	RPCIndexBlocklist = "clustarr.rpc.indexarr.blocklist"

	// The JetStream advisory and metric subjects the manager consumes
	// (design §8.2): <prefix>.<stream>.<durable>. The ack metric is a core
	// subscription, never a stream subject.
	SubjectNakAdvisoryPrefix  = "$JS.EVENT.ADVISORY.CONSUMER.MSG_NAKED"
	SubjectTermAdvisoryPrefix = "$JS.EVENT.ADVISORY.CONSUMER.MSG_TERMINATED"
	SubjectAckMetricPrefix    = "$JS.EVENT.METRIC.CONSUMER.ACK"

	// PresencePrefix begins every agent presence key in BucketProgress.
	PresencePrefix = "agent."
)

// Sizes of the new streams (design §4.3, §8.2).
const (
	IntakeMaxBytes     = 256 * MiB
	WorkEngineMaxBytes = 64 * MiB
	TaskEventsMaxBytes = 8 * MiB
	// TaskEventsMaxAge bounds an advisory nobody consumed: delivery state
	// is history, not state (design §8.4).
	TaskEventsMaxAge = time.Hour
)

// Sample frequencies (ConsumerSpec.SampleFrequency): JetStream publishes an
// ack metric for that share of a durable's acks (design §8.2).
const (
	SampleFrequencyDispatched = "10%"
	SampleFrequencyEngine     = "100%"
)

// IntakeCandidateSubject builds
// clustarr.intake.candidate.<lower(owner kind)>.<KVKeyToken(owner uid)>.
func IntakeCandidateSubject(ownerKind, ownerUID string) string {
	return "clustarr.intake.candidate." + KVKeyToken(strings.ToLower(ownerKind)) + "." + KVKeyToken(ownerUID)
}

// IntakeScanSubject builds clustarr.intake.scan.<namespace>.<KVKeyToken(scan uid)>.
func IntakeScanSubject(namespace, scanUID string) string {
	return "clustarr.intake.scan." + tok(namespace) + "." + KVKeyToken(scanUID)
}

// engineSubjectPrefix is clustarr.work.engine.<KVKeyToken(client)>.<ordinal>.
func engineSubjectPrefix(client string, ordinal int32) string {
	return "clustarr.work.engine." + KVKeyToken(client) + "." + strconv.FormatInt(int64(ordinal), 10)
}

// WorkEngineSubject builds
// clustarr.work.engine.<KVKeyToken(client)>.<ordinal>.<KVKeyToken(entry uid)>:
// one entry's command to the engine instance it is pinned to.
func WorkEngineSubject(client string, ordinal int32, entryUID string) string {
	return engineSubjectPrefix(client, ordinal) + "." + KVKeyToken(entryUID)
}

// WorkEngineResyncSubject builds the resync command's subject, last token
// "_resync": no KVKeyToken holds an underscore, so it names no entry.
func WorkEngineResyncSubject(client string, ordinal int32) string {
	return engineSubjectPrefix(client, ordinal) + "._resync"
}

// WorkEngineIDSubject builds the removal of an unidentified transfer,
// addressed by its download id: last token "_id-<KVKeyToken(downloadID)>".
func WorkEngineIDSubject(client string, ordinal int32, downloadID string) string {
	return engineSubjectPrefix(client, ordinal) + "._id-" + KVKeyToken(downloadID)
}

// EngineConsumerName is engine instance <client>-<ordinal>'s durable,
// "grabarr-engine-<KVKeyToken(client)>-<ordinal>". It is injective: the
// ordinal holds no '-', and no KVKeyToken ends in a lone '-'.
func EngineConsumerName(client string, ordinal int32) string {
	return EngineConsumerPrefix + KVKeyToken(client) + "-" + strconv.FormatInt(int64(ordinal), 10)
}

// EngineConsumer is engine instance <client>-<ordinal>'s durable on
// StreamWorkEngine (design §6.7; ruling R20's timing). It is not in
// Default(): the DownloadClient controller ensures one per rendered ordinal
// (StreamAdmin.EnsureConsumer) and deletes it when no entry is pinned to a
// removed ordinal; the engine pod binds it.
func EngineConsumer(client string, ordinal int32) ConsumerSpec {
	return ConsumerSpec{
		Name:            EngineConsumerName(client, ordinal),
		Stream:          StreamWorkEngine,
		Description:     "One engine instance's commands.",
		Filters:         []string{engineSubjectPrefix(client, ordinal) + ".>"},
		AckWait:         2 * time.Minute,
		MaxDeliver:      20,
		BackOff:         []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute},
		MaxAckPending:   4,
		Slots:           4,
		Heartbeat:       30 * time.Second,
		SampleFrequency: SampleFrequencyEngine,
		Dispatched:      true,
	}
}

// WorkImportInspectSubject builds
// clustarr.work.importarr.fileimport.inspect.<KVKeyToken(entry uid)>, under
// importarr-fileimport's filter.
func WorkImportInspectSubject(entryUID string) string {
	return "clustarr.work.importarr.fileimport.inspect." + KVKeyToken(entryUID)
}

// WorkImportExecuteSubject builds
// clustarr.work.importarr.fileimport.execute.<KVKeyToken(entry uid)>.
func WorkImportExecuteSubject(entryUID string) string {
	return "clustarr.work.importarr.fileimport.execute." + KVKeyToken(entryUID)
}

// SubjectImportRecycleFiles builds
// clustarr.work.importarr.recycle.files.<KVKeyToken(id)>, under
// importarr-recycle's filter.
func SubjectImportRecycleFiles(id string) string {
	return "clustarr.work.importarr.recycle.files." + KVKeyToken(id)
}

// MsgIDForEngineCommand is an engine command's Msg-Id,
// "engine/<entry uid>/<seq>".
func MsgIDForEngineCommand(entryUID string, seq int64) string {
	return fmt.Sprintf("engine/%s/%d", entryUID, seq)
}

// MsgIDForEngineCommandAfterBoot is a command republished after an engine
// restart, "engine/<entry uid>/<seq>/<bootID>", so the stream's dedup window
// does not swallow it.
func MsgIDForEngineCommandAfterBoot(entryUID string, seq int64, bootID string) string {
	return fmt.Sprintf("engine/%s/%d/%s", entryUID, seq, bootID)
}

// MsgIDForEngineResync is a resync command's Msg-Id,
// "engine/<client uid>/<ordinal>/resync/<resyncSeq>".
func MsgIDForEngineResync(clientUID string, ordinal int32, resyncSeq int64) string {
	return fmt.Sprintf("engine/%s/%d/resync/%d", clientUID, ordinal, resyncSeq)
}

// MsgIDForEngineRemoveID is an unidentified transfer's removal,
// "engine/<client uid>/<ordinal>/id/<downloadID>".
func MsgIDForEngineRemoveID(clientUID string, ordinal int32, downloadID string) string {
	return fmt.Sprintf("engine/%s/%d/id/%s", clientUID, ordinal, downloadID)
}

// MsgIDForImport is an import task's Msg-Id, "import/<entry uid>/<phase>/<seq>".
func MsgIDForImport(entryUID, phase string, seq int64) string {
	return fmt.Sprintf("import/%s/%s/%d", entryUID, phase, seq)
}

// MsgIDForSearch is a search task's Msg-Id, "search/<task uid>/<seq>".
func MsgIDForSearch(taskUID string, seq int64) string {
	return fmt.Sprintf("search/%s/%d", taskUID, seq)
}

// Candidate Msg-Id origins (MsgIDForCandidate).
const (
	// CandidateMsgOriginRSS scopes a candidate by its owner's UID.
	CandidateMsgOriginRSS = "rss"
	// CandidateMsgOriginPick scopes a Search pick or grabBest by the
	// Search's UID.
	CandidateMsgOriginPick = "pick"
)

// MsgIDForCandidate is an intake candidate's Msg-Id,
// "<origin>/<scope uid>/<sha1(indexer:guid)[:16]>": "rss/<owner uid>/…" or
// "pick/<search uid>/…".
func MsgIDForCandidate(origin, scopeUID, indexer, guid string) string {
	return origin + "/" + scopeUID + "/" + shortSHA1(indexer+":"+guid)
}

// MsgIDForScanObservation is a scan observation's Msg-Id,
// "scan/<scan uid>/<sha1(path)[:16]>/<fingerprint>".
func MsgIDForScanObservation(scanUID, path, fingerprint string) string {
	return "scan/" + scanUID + "/" + shortSHA1(path) + "/" + fingerprint
}

// shortSHA1 is the first 16 hex digits of s's SHA-1.
func shortSHA1(s string) string {
	sum := sha1.Sum([]byte(s)) //nolint:gosec // a dedup key, not a security boundary
	return hex.EncodeToString(sum[:])[:16]
}

// PresenceKey is an agent pod's presence key in BucketProgress,
// "agent.<KVKeyToken(domain)>.<KVKeyToken(pod)>" (design §5.3).
func PresenceKey(domain, pod string) string {
	return PresencePrefix + KVKeyToken(domain) + "." + KVKeyToken(pod)
}

// NakAdvisorySubject and TermAdvisorySubject are one durable's MSG_NAKED
// and MSG_TERMINATED advisory subjects.
func NakAdvisorySubject(stream, durable string) string {
	return SubjectNakAdvisoryPrefix + "." + stream + "." + durable
}

// TermAdvisorySubject is one durable's MSG_TERMINATED advisory subject.
func TermAdvisorySubject(stream, durable string) string {
	return SubjectTermAdvisoryPrefix + "." + stream + "." + durable
}

// AckMetricSubject is one durable's sampled ack-metric subject.
func AckMetricSubject(stream, durable string) string {
	return SubjectAckMetricPrefix + "." + stream + "." + durable
}

// dynamicDispatchedStreams are the streams whose durables come and go (one
// per transcode pool, one per engine instance), whose advisories
// TaskEventSubjects captures by a "<prefix>.<stream>.*" wildcard once the
// family's durables are Dispatched.
func dynamicDispatchedStreams() []string {
	var out []string
	if TranscodeTaskConsumer("", "").Dispatched {
		out = append(out, StreamWorkTranscode)
	}
	if EngineConsumer("", 0).Dispatched {
		out = append(out, StreamWorkEngine)
	}
	return out
}

// TaskEventSubjects is CLUSTARR_TASK_EVENTS' subjects: the MSG_NAKED and
// MSG_TERMINATED advisory subjects of every Dispatched consumer of t, and a
// "<prefix>.<stream>.*" wildcard for each stream whose dispatched durables
// are dynamic (CLUSTARR_WORK_SQUASHARR, CLUSTARR_WORK_ENGINE). Never ">":
// the stream holds only what the manager tracks.
func TaskEventSubjects(t Topology) []string {
	var out []string
	for _, c := range t.Consumers {
		if !c.Dispatched {
			continue
		}
		out = append(out, NakAdvisorySubject(c.Stream, c.Name), TermAdvisorySubject(c.Stream, c.Name))
	}
	for _, s := range dynamicDispatchedStreams() {
		out = append(out, NakAdvisorySubject(s, "*"), TermAdvisorySubject(s, "*"))
	}
	return out
}

// agentStreams are the intake and command streams (design §4.3). The
// task-events stream is built by taskEventsStream from the finished
// topology.
func agentStreams() []StreamSpec {
	return []StreamSpec{
		{
			Name:        StreamIntake,
			Description: "Candidates and scan observations agents propose; the manager decides.",
			Subjects:    []string{FilterIntake},
			Retention:   RetentionWorkQueue,
			Storage:     StorageFile,
			Discard:     DiscardNew,
			MaxBytes:    IntakeMaxBytes,
			Duplicates:  time.Hour,
			Replicas:    3,
			Durable:     true,
		},
		{
			Name:        StreamWorkEngine,
			Description: "The manager's desired-state commands to engine instances.",
			Subjects:    []string{FilterWorkEngine},
			Retention:   RetentionWorkQueue,
			Storage:     StorageFile,
			Discard:     DiscardNew,
			MaxBytes:    WorkEngineMaxBytes,
			Duplicates:  time.Hour,
			Replicas:    3,
			Durable:     true,
		},
	}
}

// taskEventsStream is CLUSTARR_TASK_EVENTS over subjects (TaskEventSubjects):
// not Durable, so a single node keeps it in memory at the 1 MiB floor;
// DiscardOld and an hour's MaxAge, since a lost advisory leaves delivery
// state stale only until the next transition (design §8.4).
func taskEventsStream(subjects []string) StreamSpec {
	return StreamSpec{
		Name:        StreamTaskEvents,
		Description: "Nak and term advisories of every task the manager dispatches.",
		Subjects:    subjects,
		Retention:   RetentionWorkQueue,
		Storage:     StorageFile,
		Discard:     DiscardOld,
		MaxAge:      TaskEventsMaxAge,
		MaxBytes:    TaskEventsMaxBytes,
		Replicas:    3,
	}
}

// agentConsumers are the manager's intake durables (ruling R20's timing).
func agentConsumers() []ConsumerSpec {
	s := time.Second
	return []ConsumerSpec{
		{
			Name: ConsumerIntakeCandidate, Stream: StreamIntake,
			Description: "Grab candidates, held for their owner's decision (leader-only, the manager).",
			Filters:     []string{FilterIntakeCandidates},
			AckWait:     30 * s, MaxDeliver: 20,
			BackOff:       []time.Duration{10 * s},
			MaxAckPending: 64, Slots: 64,
			Heartbeat: 10 * s, HandlerTimeout: 60 * s,
		},
		{
			Name: ConsumerIntakeScan, Stream: StreamIntake,
			Description: "Library scan observations (leader-only, the manager's scan applier).",
			Filters:     []string{FilterIntakeScans},
			AckWait:     30 * s, MaxDeliver: 10,
			BackOff:       []time.Duration{5 * s, 30 * s, 2 * time.Minute},
			MaxAckPending: 64, Slots: 8,
			Heartbeat: 10 * s, HandlerTimeout: 60 * s,
		},
	}
}

// taskEventsConsumer is the manager's advisory intake over subjects.
func taskEventsConsumer(subjects []string) ConsumerSpec {
	return ConsumerSpec{
		Name: ConsumerTaskEvents, Stream: StreamTaskEvents,
		Description: "Nak and term advisories to delivery state (leader-only, the manager).",
		Filters:     append([]string(nil), subjects...),
		AckWait:     30 * time.Second, MaxDeliver: 3,
		BackOff:       []time.Duration{5 * time.Second},
		MaxAckPending: 256, Slots: 16,
	}
}

// withAgentTopology adds the intake and command streams and durables to t,
// then the task-events stream and its durable over t's Dispatched consumers.
// Every added durable gets its dead-letter watcher.
func withAgentTopology(t Topology) Topology {
	t.Streams = append(t.Streams, agentStreams()...)
	t.Consumers = append(t.Consumers, withDeadLetterWatchers(agentConsumers())...)
	subjects := TaskEventSubjects(t)
	t.Streams = append(t.Streams, taskEventsStream(subjects))
	t.Consumers = append(t.Consumers, withDeadLetterWatchers([]ConsumerSpec{taskEventsConsumer(subjects)})...)
	t.Buckets = append(t.Buckets, agentRecordBuckets()...)
	return t
}
