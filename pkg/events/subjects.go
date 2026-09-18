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
	StreamWorkIndexarr   = "CLUSTARR_WORK_INDEXARR"
	StreamWorkCaptionarr = "CLUSTARR_WORK_CAPTIONARR"
	StreamDLQ            = "CLUSTARR_DLQ"
)

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
	FilterWorkIndexarr    = "clustarr.work.indexarr.>"
	FilterWorkCaptionarr  = "clustarr.work.captionarr.>"
	FilterAllDLQ          = "clustarr.dlq.>"
	FilterCatalogSearch   = "clustarr.work.catalogarr.search.>"
	FilterCatalogGrab     = "clustarr.work.catalogarr.grab.>"
	FilterCatalogImport   = "clustarr.work.catalogarr.import.>"
	FilterCatalogMetadata = "clustarr.work.catalogarr.metadata.>"
	FilterCatalogList     = "clustarr.work.catalogarr.importlist.>"
	FilterCatalogWanted   = "clustarr.work.catalogarr.wantedscan.>"
	FilterIndexRSS        = "clustarr.work.indexarr.rss.>"
	FilterIndexDefs       = "clustarr.work.indexarr.definitions.>"
	FilterCaptionFetch    = "clustarr.work.captionarr.fetch.>"
)

// Durable consumer names.
const (
	ConsumerCatalogRSSMatcher  = "catalogarr-rss-matcher"
	ConsumerCatalogSearchHigh  = "catalogarr-search-high"
	ConsumerCatalogSearchNorm  = "catalogarr-search-normal"
	ConsumerCatalogGrab        = "catalogarr-grab"
	ConsumerCatalogImport      = "catalogarr-import"
	ConsumerCatalogMetadata    = "catalogarr-metadata"
	ConsumerCatalogImportList  = "catalogarr-importlist"
	ConsumerCatalogHistory     = "catalogarr-history"
	ConsumerIndexRSS           = "indexarr-rss"
	ConsumerIndexDefinitions   = "indexarr-definitions"
	ConsumerCaptionFetchHigh   = "captionarr-fetch-high"
	ConsumerCaptionFetchNormal = "captionarr-fetch-normal"
	ConsumerDLQProjector       = "clustarr-dlq-projector"
)

// Key/value bucket names. NATS bucket names may not contain dots.
const (
	BucketLeases           = "clustarr-leases"
	BucketPending          = "clustarr-pending"
	BucketIndexerSessions  = "clustarr-indexer-sessions"
	BucketIndexerLimits    = "clustarr-indexer-limits"
	BucketProviderThrottle = "clustarr-provider-throttle"
	BucketMetadataCache    = "clustarr-metadata-cache"
	BucketSearchCache      = "clustarr-search-cache"
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

// WorkImportSubject builds
// clustarr.work.catalogarr.import.normal.<download-uid>.
func WorkImportSubject(downloadUID string) string {
	return "clustarr.work.catalogarr.import.normal." + tok(downloadUID)
}

// WorkMetadataSubject builds
// clustarr.work.catalogarr.metadata.<high|normal>.<mediaKey>.
func WorkMetadataSubject(p Priority, mediaKey string) string {
	return fmt.Sprintf("clustarr.work.catalogarr.metadata.%s.%s", tok(string(p)), tok(mediaKey))
}

// WorkImportListSubject builds
// clustarr.work.catalogarr.importlist.normal.<uid>.
func WorkImportListSubject(uid string) string {
	return "clustarr.work.catalogarr.importlist.normal." + tok(uid)
}

// WorkWantedScanSubject builds
// clustarr.work.catalogarr.wantedscan.low.<namespace>.
func WorkWantedScanSubject(namespace string) string {
	return "clustarr.work.catalogarr.wantedscan.low." + tok(namespace)
}

// WorkRSSSubject builds clustarr.work.indexarr.rss.normal.<indexer-uid>.
func WorkRSSSubject(indexerUID string) string {
	return "clustarr.work.indexarr.rss.normal." + tok(indexerUID)
}

// WorkDefinitionsSubject is the single definitions-sync work subject.
const WorkDefinitionsSubject = "clustarr.work.indexarr.definitions.normal.sync"

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
func LeaseKey(mediaKey string) string { return "grab." + kvTok(mediaKey) }

// PendingKey builds the clustarr-pending key holding the best candidate so
// far for a media key.
func PendingKey(mediaKey string) string { return "grab." + kvTok(mediaKey) }

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

// MsgIDForSubtitle builds the deduplication ID for a subtitle fetch task:
// "<request-uid>/<langKey>/<probeHash>". A re-probe that yields the same hash
// does not re-enqueue the fetch.
func MsgIDForSubtitle(requestUID, langKey, probeHash string) string {
	return fmt.Sprintf("%s/%s/%s", requestUID, langKey, probeHash)
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

// kvTok sanitises a key/value key segment. KV keys allow dots but not
// wildcards or whitespace.
func kvTok(s string) string {
	if s == "" {
		return "_"
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '*', '>', '/', '\\':
			return '-'
		}
		if r < 0x20 || r == 0x7f {
			return '-'
		}
		return r
	}, s)
}
