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
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
)

// Stream names.
const (
	StreamEvents         = "CLUSTARR_EVENTS"
	StreamReleases       = "CLUSTARR_RELEASES"
	StreamWorkCatalogarr = "CLUSTARR_WORK_CATALOGARR"
	StreamWorkImportarr  = "CLUSTARR_WORK_IMPORTARR"
	StreamWorkIndexarr   = "CLUSTARR_WORK_INDEXARR"
	StreamWorkCaptionarr = "CLUSTARR_WORK_CAPTIONARR"
	StreamDLQ            = "CLUSTARR_DLQ"

	// StreamAdvisories keeps JetStream's MAX_DELIVERIES advisories until a
	// replica of the consumer they name has dead-lettered the message. See
	// FilterMaxDeliveriesAdvisories.
	StreamAdvisories = "CLUSTARR_ADVISORIES"
)

// The MAX_DELIVERIES advisory. JetStream publishes one, at
// SubjectMaxDeliveriesAdvisoryPrefix.<stream>.<consumer>, when it gives up on
// a message whose final delivery lapsed without a settlement: the handler was
// still running at the last acknowledgement deadline, or it naked the
// delivery. The server gives up on the message at that moment, whatever the
// client is doing, and the advisory is the only record of it; natsbus
// dead-letters the message from it (spec §5, gap fixes Y1).
//
// The advisory is core NATS, so on its own it reaches only a subscriber
// listening when it fires, and JetStream can fire it with none: it fires
// when it next tries to deliver the message, which a pull request the
// consumer's last replica left behind as it stopped is enough for. Capturing
// it in StreamAdvisories keeps it until a replica is back (gap fixes Z2).
// The prefix is nats-server's JSAdvisoryConsumerMaxDeliveryExceedPre,
// restated so the production binary does not link the server; natsbus's
// tests hold the two equal.
const (
	SubjectMaxDeliveriesAdvisoryPrefix = "$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES"
	FilterMaxDeliveriesAdvisories      = SubjectMaxDeliveriesAdvisoryPrefix + ".>"
)

// MaxDeliveriesAdvisorySubject is the subject JetStream announces a lapsed
// final delivery of durable on stream under. Stream and consumer names cannot
// contain a dot, so it is unambiguous.
func MaxDeliveriesAdvisorySubject(stream, durable string) string {
	return SubjectMaxDeliveriesAdvisoryPrefix + "." + stream + "." + durable
}

// Subject roots. Every Clustarr subject starts with one of these.
const (
	SubjectRoot           = "clustarr"
	SubjectEventPrefix    = "clustarr.evt."
	SubjectReleasePrefix  = "clustarr.rel."
	SubjectWorkPrefix     = "clustarr.work."
	SubjectRPCPrefix      = "clustarr.rpc."
	SubjectProgressPrefix = "clustarr.progress."
	SubjectDLQPrefix      = "clustarr.dlq."
)

// Stream subject filters, as configured on the streams themselves.
const (
	FilterAllEvents       = "clustarr.evt.>"
	FilterAllReleases     = "clustarr.rel.>"
	FilterWorkCatalogarr  = "clustarr.work.catalogarr.>"
	FilterWorkImportarr   = "clustarr.work.importarr.>"
	FilterWorkIndexarr    = "clustarr.work.indexarr.>"
	FilterWorkCaptionarr  = "clustarr.work.captionarr.>"
	FilterAllDLQ          = "clustarr.dlq.>"
	FilterCatalogSearch   = "clustarr.work.catalogarr.search.>"
	FilterCatalogGrab     = "clustarr.work.catalogarr.grab.>"
	FilterCatalogMetadata = "clustarr.work.catalogarr.metadata.>"
	FilterCatalogWanted   = "clustarr.work.catalogarr.wantedscan.>"
	FilterImportScan      = "clustarr.work.importarr.scan.>"
	FilterImportList      = "clustarr.work.importarr.list.>"
	FilterImportFile      = "clustarr.work.importarr.fileimport.>"
	FilterIndexRSS        = "clustarr.work.indexarr.rss.>"
	FilterCaptionFetch    = "clustarr.work.captionarr.fetch.>"
)

// Durable consumer names.
const (
	ConsumerCatalogRSSMatcher  = "catalogarr-rss-matcher"
	ConsumerCatalogSearchHigh  = "catalogarr-search-high"
	ConsumerCatalogSearchNorm  = "catalogarr-search-normal"
	ConsumerCatalogGrab        = "catalogarr-grab"
	ConsumerCatalogMetadata    = "catalogarr-metadata"
	ConsumerCatalogHistory     = "catalogarr-history"
	ConsumerImportScan         = "importarr-scan"
	ConsumerImportList         = "importarr-list"
	ConsumerImportFile         = "importarr-fileimport"
	ConsumerIndexRSS           = "indexarr-rss"
	ConsumerCaptionFetchHigh   = "captionarr-fetch-high"
	ConsumerCaptionFetchNormal = "captionarr-fetch-normal"
	ConsumerDLQProjector       = "clustarr-dlq-projector"
)

// Key/value bucket names. NATS bucket names may not contain dots.
const (
	BucketLeases           = "clustarr-leases"
	BucketPending          = "clustarr-pending"
	BucketImportExclusions = "clustarr-import-exclusions"
	BucketIndexerSessions  = "clustarr-indexer-sessions"
	BucketIndexerLimits    = "clustarr-indexer-limits"
	BucketProviderThrottle = "clustarr-provider-throttle"
	BucketMetadataCache    = "clustarr-metadata-cache"
	BucketProgress         = "clustarr-progress"
	BucketImportList       = "clustarr-importlist"
	BucketDedup            = "clustarr-dedup"
)

// Priority is the work-queue lane a task is placed in. It is the third token
// of every clustarr.work.* subject and decides which durable consumer, and so
// which worker pool and concurrency budget, picks the task up.
type Priority string

// Work-queue priorities.
const (
	PriorityHigh   Priority = "high"
	PriorityNormal Priority = "normal"
	PriorityLow    Priority = "low"
)

// Valid reports whether p is one of the three known lanes.
func (p Priority) Valid() bool {
	switch p {
	case PriorityHigh, PriorityNormal, PriorityLow:
		return true
	}
	return false
}

// Event subject actions.
const (
	ActionAdded       = "added"
	ActionUpdated     = "updated"
	ActionDeleted     = "deleted"
	ActionGrabbed     = "grabbed"
	ActionRejected    = "rejected"
	ActionImported    = "imported"
	ActionReplaced    = "replaced"
	ActionSynced      = "synced"
	ActionDisabled    = "disabled"
	ActionRecovered   = "recovered"
	ActionLimited     = "limited"
	ActionQueued      = "queued"
	ActionStarted     = "started"
	ActionCompleted   = "completed"
	ActionSeedGoalMet = "seedGoalMet"
	ActionFailed      = "failed"
	ActionBlocklisted = "blocklisted"
	ActionRemoved     = "removed"
	ActionSucceeded   = "succeeded"
	ActionSkipped     = "skipped"
	ActionDownloaded  = "downloaded"
	ActionUpgraded    = "upgraded"
)

// RPC subjects. Each is served by a queue group named after the service.
const (
	RPCIndexSearch      = "clustarr.rpc.indexarr.search"
	RPCIndexDownload    = "clustarr.rpc.indexarr.download"
	RPCIndexQuery       = "clustarr.rpc.indexarr.query"
	RPCMetadataLookup   = "clustarr.rpc.catalogarr.metadata.lookup"
	RPCMetadataSearch   = "clustarr.rpc.catalogarr.metadata.search"
	RPCMetadataResolve  = "clustarr.rpc.catalogarr.metadata.resolve"
	QueueGroupIndexarr  = "indexarr"
	QueueGroupCatalogar = "catalogarr"
)

// CatalogItemSubject builds clustarr.evt.catalog.<kind>.<action>.<uid>.
func CatalogItemSubject(kind, action, uid string) string {
	return fmt.Sprintf("clustarr.evt.catalog.%s.%s.%s", tok(kind), tok(action), tok(uid))
}

// CatalogReleaseSubject builds
// clustarr.evt.catalog.release.<grabbed|rejected>.<target-uid>.
func CatalogReleaseSubject(action, targetUID string) string {
	return fmt.Sprintf("clustarr.evt.catalog.release.%s.%s", tok(action), tok(targetUID))
}

// CatalogMediaFileSubject builds
// clustarr.evt.catalog.mediafile.<imported|replaced|deleted>.<uid>.
//
// uid is the catalog item's UID, not the MediaFile's: the Movie and Episode
// reconcilers publish these as their fileRef changes, and the reconciler
// that sees a MediaFile deleted no longer has its UID. It matches
// CatalogReleaseSubject's <target-uid>.
func CatalogMediaFileSubject(action, uid string) string {
	return fmt.Sprintf("clustarr.evt.catalog.mediafile.%s.%s", tok(action), tok(uid))
}

// CatalogImportListSyncedSubject builds
// clustarr.evt.catalog.importlist.synced.<uid>.
func CatalogImportListSyncedSubject(uid string) string {
	return "clustarr.evt.catalog.importlist.synced." + tok(uid)
}

// ReleaseSubject builds clustarr.rel.<protocol>.<indexerName>.<newznabTop>,
// the RSS fan-out subject indexarr publishes parsed releases on.
func ReleaseSubject(protocol, indexerName string, newznabTop int) string {
	return fmt.Sprintf("clustarr.rel.%s.%s.%d", tok(protocol), tok(indexerName), newznabTop)
}

// IndexerEventSubject builds
// clustarr.evt.index.indexer.<disabled|recovered|limited>.<uid>.
func IndexerEventSubject(action, uid string) string {
	return fmt.Sprintf("clustarr.evt.index.indexer.%s.%s", tok(action), tok(uid))
}

// DownloadEventSubject builds clustarr.evt.download.download.<action>.<uid>.
func DownloadEventSubject(action, uid string) string {
	return fmt.Sprintf("clustarr.evt.download.download.%s.%s", tok(action), tok(uid))
}

// TranscodeJobSubject builds clustarr.evt.transcode.job.<action>.<uid>.
func TranscodeJobSubject(action, uid string) string {
	return fmt.Sprintf("clustarr.evt.transcode.job.%s.%s", tok(action), tok(uid))
}

// SubtitleEventSubject builds
// clustarr.evt.subtitle.subtitle.<action>.<request-uid>.
func SubtitleEventSubject(action, requestUID string) string {
	return fmt.Sprintf("clustarr.evt.subtitle.subtitle.%s.%s", tok(action), tok(requestUID))
}

// WorkSearchSubject builds
// clustarr.work.catalogarr.search.<priority>.<mediaKey>.
func WorkSearchSubject(p Priority, mediaKey string) string {
	return fmt.Sprintf("clustarr.work.catalogarr.search.%s.%s", tok(string(p)), tok(mediaKey))
}

// WorkGrabSubject builds clustarr.work.catalogarr.grab.normal.<mediaKey>.
// Grab tasks are published with WithScheduleAt to honour delay profiles.
func WorkGrabSubject(mediaKey string) string {
	return "clustarr.work.catalogarr.grab.normal." + tok(mediaKey)
}

// WorkMetadataSubject builds
// clustarr.work.catalogarr.metadata.<high|normal>.<mediaKey>.
func WorkMetadataSubject(p Priority, mediaKey string) string {
	return fmt.Sprintf("clustarr.work.catalogarr.metadata.%s.%s", tok(string(p)), tok(mediaKey))
}

// WorkWantedScanSubject builds
// clustarr.work.catalogarr.wantedscan.low.<namespace>.
func WorkWantedScanSubject(namespace string) string {
	return "clustarr.work.catalogarr.wantedscan.low." + tok(namespace)
}

// The three importarr work subjects (amendment §A1.6). Unlike the catalogarr
// and captionarr work subjects they carry no priority token: the amendment
// spells them out as work.importarr.scan.<rootfolder>,
// work.importarr.list.<importlist> and work.importarr.fileimport.<download>,
// and there are no high/normal/low lanes to choose between -- one consumer
// drains each task type. They are still clustarr.work.<service>.<task>.<id>,
// five tokens, so [ScheduleSubject] accepts them.

// WorkScanSubject builds clustarr.work.importarr.scan.<rootfolder>. A scan of
// a large library is chunked by directory, so rootFolder here identifies the
// scan's root and the chunk rides in the payload, not the subject: a subject
// token per directory would make the work-queue stream's subject cardinality
// grow with the library.
func WorkScanSubject(rootFolder string) string {
	return "clustarr.work.importarr.scan." + tok(rootFolder)
}

// WorkListSubject builds clustarr.work.importarr.list.<importlist>.
func WorkListSubject(importList string) string {
	return "clustarr.work.importarr.list." + tok(importList)
}

// WorkFileImportSubject builds
// clustarr.work.importarr.fileimport.<download>.
func WorkFileImportSubject(downloadUID string) string {
	return "clustarr.work.importarr.fileimport." + tok(downloadUID)
}

// WorkRSSSubject builds clustarr.work.indexarr.rss.normal.<indexer-uid>.
func WorkRSSSubject(indexerUID string) string {
	return "clustarr.work.indexarr.rss.normal." + tok(indexerUID)
}

// WorkFetchSubject builds
// clustarr.work.captionarr.fetch.<priority>.<request-uid>.<langKey>.
func WorkFetchSubject(p Priority, requestUID, langKey string) string {
	return fmt.Sprintf("clustarr.work.captionarr.fetch.%s.%s.%s",
		tok(string(p)), tok(requestUID), tok(langKey))
}

// ScheduleSubject returns the holding subject for a scheduled publish to
// target. A scheduled message is stored on the holding subject and the broker
// republishes it to target when the schedule fires, so the two must differ or
// the message would re-trigger itself.
//
// The holding subject inserts a "sched" token after the service, turning
// clustarr.work.catalogarr.grab.normal.<key> into
// clustarr.work.catalogarr.sched.grab.normal.<key>. It therefore stays inside
// the same work stream while matching no consumer's filters, which all name a
// task directly. Only clustarr.work.* subjects can be scheduled, because they
// are the only streams with AllowMsgSchedules.
func ScheduleSubject(target string) (string, error) {
	parts := strings.Split(target, ".")
	if len(parts) < 5 || parts[0] != "clustarr" || parts[1] != "work" {
		return "", fmt.Errorf(
			"events: %q cannot be scheduled: only clustarr.work.<service>.<task>.* subjects support schedules",
			target)
	}
	head := strings.Join(parts[:3], ".")
	tail := strings.Join(parts[3:], ".")
	return head + ".sched." + tail, nil
}

// ProgressDownloadSubject builds clustarr.progress.download.<uid>. Progress
// is core NATS at 1 Hz and is never persisted to a stream.
func ProgressDownloadSubject(uid string) string {
	return "clustarr.progress.download." + tok(uid)
}

// ProgressTranscodeSubject builds clustarr.progress.transcode.<uid>.
func ProgressTranscodeSubject(uid string) string {
	return "clustarr.progress.transcode." + tok(uid)
}

// DLQSubject builds clustarr.dlq.<service>.<task>.<id>.
func DLQSubject(service, task, id string) string {
	return fmt.Sprintf("clustarr.dlq.%s.%s.%s", tok(service), tok(task), tok(id))
}

// LeaseKey builds the clustarr-leases key guarding a single media key against
// a double grab.
//
// The media key goes through [KVKeyToken] rather than being interpolated
// raw. It is tok()-stable already, so today every byte it carries is legal
// in a KV key -- but only by accident of [MediaKey] flattening a
// DNS-1123 namespace and name for the WIRE, which is a different grammar
// with different forbidden characters that happens to be stricter. Escaping
// here makes the key legal by construction instead of by coincidence.
func LeaseKey(mediaKey string) string { return "grab." + KVKeyToken(mediaKey) }

// PendingKey builds the clustarr-pending key holding the best candidate so
// far for a media key. See [LeaseKey] for why the media key is escaped.
func PendingKey(mediaKey string) string { return "grab." + KVKeyToken(mediaKey) }

// ExclusionKey is the clustarr-import-exclusions key for one exclusion. The
// import-list and search paths consult this bucket rather than listing
// ImportExclusion resources on every candidate, which would not scale.
//
// Both segments go through [KVKeyToken], and unlike [LeaseKey] that is not a
// precaution: id comes straight out of ImportExclusion.spec.externalIDs, an
// unconstrained map[string]string. "tt0113277:2", "Amélie" and "50%" are all
// ids a user can type, all three are rejected by nats.go's key grammar, and
// the resulting object is not merely un-published but UNDELETABLE -- the
// finalizer's kv.Delete validates the same key and fails the same way
// forever. See [KVKeyToken].
func ExclusionKey(source, id string) string {
	return KVKeyToken(source) + "." + KVKeyToken(id)
}

// MsgIDForObject builds the deduplication ID for a task derived from a custom
// resource: "<uid>:<generation>:<task>". Republishing the same generation of
// the same object for the same task is a no-op inside the stream's
// deduplication window.
func MsgIDForObject(uid string, generation int64, task string) string {
	return fmt.Sprintf("%s:%d:%s", uid, generation, task)
}

// MsgIDForRelease builds the deduplication ID for an RSS release:
// sha1("<indexerName>:<guid>"). Re-reading the same RSS page does not
// republish releases already seen.
func MsgIDForRelease(indexerName, guid string) string {
	sum := sha1.Sum([]byte(indexerName + ":" + guid))
	return hex.EncodeToString(sum[:])
}

// MsgIDForSubtitle builds the deduplication ID for a scheduled subtitle
// fetch task: "<request-uid>/<langKey>/<probeHash>/<attempt>", where attempt
// is the number the dispatch is recorded as in the item's attempts.count.
//
// Spec §6.5 names only "<uid>/<langKey>/<probeHash>". The attempt was added
// by plan task F-6: without it every dispatch of one language for one file
// shared one ID, so the work stream's one-hour deduplication window absorbed
// every search that came due within an hour of the previous one -- a
// search.interval under an hour silently became an hour. What the
// deterministic ID exists for still holds: a republish of the SAME attempt
// (a status apply that failed after the publish, a reconcile from a lagging
// cache) is absorbed, and a re-probe that yields the same hash does not
// re-enqueue the fetch. Only the next scheduled search, which is a new
// attempt, gets a new ID.
func MsgIDForSubtitle(requestUID, langKey, probeHash string, attempt int32) string {
	return fmt.Sprintf("%s/%s/%s/%d", requestUID, langKey, probeHash, attempt)
}

// MsgIDForForcedSubtitle builds the deduplication ID for a fetch task sent
// because SubtitleRequest.spec.forceSearch was set:
// "<request-uid>/<langKey>/<probeHash>/force-<generation>", where
// generation is the request's metadata.generation while forceSearch is true.
//
// Setting forceSearch bumps the generation and the controller's reset bumps
// it again, so every user-forced search has an ID of its own and is never
// absorbed by the dispatch before it -- the "search now" that under the
// three-part ID did nothing for an hour. Repeats of one forced search (a
// reset that failed and was retried) share the generation and are absorbed.
func MsgIDForForcedSubtitle(requestUID, langKey, probeHash string, generation int64) string {
	return fmt.Sprintf("%s/%s/%s/force-%d", requestUID, langKey, probeHash, generation)
}

// SubjectMatches reports whether subject matches a NATS subject filter, with
// "*" matching one token and a trailing ">" matching one or more.
func SubjectMatches(filter, subject string) bool {
	if filter == "" || filter == ">" {
		return subject != ""
	}
	f := strings.Split(filter, ".")
	s := strings.Split(subject, ".")
	for i, ft := range f {
		if ft == ">" {
			return i < len(s)
		}
		if i >= len(s) {
			return false
		}
		if ft != "*" && ft != s[i] {
			return false
		}
	}
	return len(f) == len(s)
}

// tok sanitises a subject token: NATS forbids dots, spaces and the wildcard
// characters inside a token, so they collapse to "-".
func tok(s string) string {
	if s == "" {
		return "_"
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '.', ' ', '\t', '*', '>', '/', '\\':
			return '-'
		}
		if r < 0x20 || r == 0x7f {
			return '-'
		}
		return r
	}, s)
}

// The KV key builders above used to share a kvTok() here that mapped only
// space, tab, "*", ">", "/", "\" and the control characters and passed
// everything else through, which implemented no grammar at all -- ":", "%",
// "," and every non-ASCII byte reached NATS untouched and were rejected. It
// is gone; [KVKeyToken] in kvkey.go is the single implementation.
