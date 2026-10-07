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
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Stream names.
const (
	StreamEvents        = "CLUSTARR_EVENTS"
	StreamReleases      = "CLUSTARR_RELEASES"
	StreamWorkCatalog   = "CLUSTARR_WORK_CATALOG"
	StreamWorkImport    = "CLUSTARR_WORK_IMPORT"
	StreamWorkIndex     = "CLUSTARR_WORK_INDEX"
	StreamWorkCaption   = "CLUSTARR_WORK_CAPTION"
	StreamWorkTranscode = "CLUSTARR_WORK_TRANSCODE"
	// StreamWorkMarkers carries skip-segment work: TheIntroDB's marker
	// fetches and segment detection's plans, analysis tasks and results.
	StreamWorkMarkers = "CLUSTARR_WORK_MARKERS"
	StreamDLQ         = "CLUSTARR_DLQ"

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
	FilterAllEvents           = "clustarr.evt.>"
	FilterAllReleases         = "clustarr.rel.>"
	FilterWorkCatalog         = "clustarr.work.catalog.>"
	FilterWorkImport          = "clustarr.work.import.>"
	FilterWorkIndex           = "clustarr.work.index.>"
	FilterWorkCaption         = "clustarr.work.caption.>"
	FilterWorkTranscode       = "clustarr.work.transcode.>"
	FilterWorkMarkers         = "clustarr.work.markers.>"
	FilterAllDLQ              = "clustarr.dlq.>"
	FilterCatalogSearch       = "clustarr.work.catalog.search.>"
	FilterCatalogMetadata     = "clustarr.work.catalog.metadata.>"
	FilterCatalogWanted       = "clustarr.work.catalog.wantedscan.>"
	FilterImportScan          = "clustarr.work.import.scan.>"
	FilterImportList          = "clustarr.work.import.list.>"
	FilterImportFile          = "clustarr.work.import.fileimport.>"
	FilterIndexRSS            = "clustarr.work.index.rss.>"
	FilterCaptionFetch        = "clustarr.work.caption.fetch.>"
	FilterCatalogArtworkFetch = "clustarr.work.catalog.artwork.fetch.>"
	FilterCatalogMarkers      = "clustarr.work.markers.markers.>"
	// Segment detection (spec 2026-10-01): a season's or a movie's plan and
	// the analysis task the worker takes.
	FilterCatalogSegmentsPlan    = "clustarr.work.markers.plan.>"
	FilterCatalogSegmentsAnalyze = "clustarr.work.markers.analyze.>"
	FilterCatalogArtworkRender   = "clustarr.work.catalog.artwork.render.>"
	// FilterImportRecycle is the recycle-bin sweep the manager queues
	// (spec 2026-10-06 §3.5.3). It lies inside FilterWorkImport, so the
	// stream is unchanged.
	FilterImportRecycle = "clustarr.work.import.recycle.>"
)

// Durable consumer names.
const (
	ConsumerCatalogRSSMatcher    = "catalog-rss-matcher"
	ConsumerCatalogSearchHigh    = "catalog-search-high"
	ConsumerCatalogSearchNorm    = "catalog-search-normal"
	ConsumerCatalogMetadata      = "catalog-metadata"
	ConsumerCatalogHistory       = "catalog-history"
	ConsumerImportScan           = "import-scan"
	ConsumerImportList           = "import-list"
	ConsumerImportFile           = "import-fileimport"
	ConsumerImportRecycle        = "import-recycle"
	ConsumerIndexRSS             = "index-rss"
	ConsumerCaptionFetchHigh     = "caption-fetch-high"
	ConsumerCaptionFetchNormal   = "caption-fetch-normal"
	ConsumerDLQProjector         = "clustarr-dlq-projector"
	ConsumerCatalogArtworkFetch  = "catalog-artwork-fetch"
	ConsumerCatalogArtworkRender = "catalog-artwork-render"
	ConsumerCatalogMarkers       = "catalog-markers"
	// ConsumerCatalogSegmentsPlan is catalog's; ConsumerMarkersAnalyze
	// is markers-worker's.
	ConsumerCatalogSegmentsPlan = "catalog-segments-plan"
	ConsumerMarkersAnalyze      = "markers-analyze"
)

// Key/value bucket names. NATS bucket names may not contain dots.
const (
	BucketImportExclusions = "clustarr-import-exclusions"
	BucketIndexerSessions  = "clustarr-indexer-sessions"
	BucketIndexerLimits    = "clustarr-indexer-limits"
	BucketProviderThrottle = "clustarr-provider-throttle"
	BucketMetadataCache    = "clustarr-metadata-cache"
	// BucketMetadataExtended holds the people and similar titles per
	// catalog item that the Plex provider shows and no CRD carries (spec
	// 2026-09-30 plex-full-metadata-response §4).
	BucketMetadataExtended = "clustarr-metadata-extended"
	// BucketPlexExtras holds the extras Plex's metadata service holds per
	// Plex id, which the metadata gateway fetches on a miss and the ui's
	// extras route answers PMS with (pkg/metadata/plexextras).
	BucketPlexExtras      = "clustarr-plex-extras"
	BucketProgress        = "clustarr-progress"
	BucketImportList      = "clustarr-importlist"
	BucketDedup           = "clustarr-dedup"
	BucketTranscodeLeases = "clustarr-transcode-leases"
)

// BucketArtwork is the object-store bucket holding artwork originals and
// overlays (spec §B.2). Unlike the key/value buckets above it holds objects
// named by ArtworkKey, but the same NATS bucket-name rule applies: no dots.
const BucketArtwork = "clustarr-artwork"

// ArtworkMaxBytes is the artwork bucket's byte limit, spec §B.2.
const ArtworkMaxBytes int64 = 5 * GiB

// BucketSegments holds each MediaFile's raw segment analysis, keyed by its
// UID through KVKeyToken.
const BucketSegments = "clustarr-segments"

// ObjectStoreFingerprints holds segment detection's audio fingerprints,
// "<probeHash>.start" and "<probeHash>.end".
const ObjectStoreFingerprints = "clustarr-fingerprints"

// ArtworkChunkSize is BucketArtwork's object chunk size: 1 MiB, so an image
// of up to 3 MB is at most 3 chunks (ADR-0020 §5).
const ArtworkChunkSize uint32 = 1 << 20

// FingerprintsMaxBytes bounds ObjectStoreFingerprints: about 20 KB per
// window, so room for some 25,000 files.
const FingerprintsMaxBytes int64 = 1 * GiB

// BucketLiveTV holds the Live TV tuner's objects (spec 2026-10-07 §5.4):
// each provider's playlist (without stream URLs), each guide's channel list
// and the rendered XEPG guide. The tuner writes it alone; the ui only reads.
const BucketLiveTV = "clustarr-livetv"

// LiveTVMaxBytes bounds BucketLiveTV.
const LiveTVMaxBytes int64 = 1 * GiB

// LiveTVKeyScheme is BucketLiveTV's key scheme, recorded in the bucket's
// metadata.
const LiveTVKeyScheme = "playlist/<ns>/<name> | guide-channels/<ns>/<name>/<guide> | guide/<ns>/<name>"

// ArtworkVariant is "original" or "overlay", the last token of an
// ArtworkKey and the Clustarr-Rendered-From-bearing half of the split
// between the metadata gateway and the renderer (spec §B.2, §B.3).
const (
	ArtworkVariantOriginal = "original"
	ArtworkVariantOverlay  = "overlay"
)

// ArtworkMetaVersion is the version of the metadata set every artwork object
// carries (artwork design §B.2 as amended 2026-10-07), recorded under
// ArtworkMetaKeyVersion. Raise it when the set changes: the item key plans a
// backfill for an object below it, which its writer makes with SetMeta.
// 2 (ADR-0019 A5.2, D13) adds the source, source URL and update time every
// object carries and an overlay's inputs digest, from which the manager
// renders status.artwork and status.overlay.
const ArtworkMetaVersion = "2"

// ArtworkMaxImageBytes caps one artwork image: the gateway's fetch limit
// (artwork design §B.4) and the ui's whole-object read bound (§B.8 as
// amended), here so both can name it.
const ArtworkMaxImageBytes = 20 << 20

// The metadata keys of an artwork object (artwork design §B.2 as amended
// 2026-10-07). Every object carries the version, kind, uid, namespace,
// name, image type and variant; language when its provider image names
// one; width and height when known; and an overlay its profile and the
// digest of the original it was drawn on. Since version 2 every object also
// carries its source (provider, custom or render), its source URL and when
// it was stored (RFC 3339, UTC, seconds), and an overlay the inputs digest
// it was rendered from (ADR-0019 D13). BucketMetaKeyKeyScheme is a
// bucket's, not an object's.
const (
	ArtworkMetaKeyVersion        = "clustarr.io/meta-version"
	ArtworkMetaKeyKind           = "clustarr.io/kind"
	ArtworkMetaKeyUID            = "clustarr.io/uid"
	ArtworkMetaKeyNamespace      = "clustarr.io/namespace"
	ArtworkMetaKeyName           = "clustarr.io/name"
	ArtworkMetaKeyImageType      = "clustarr.io/image-type"
	ArtworkMetaKeyVariant        = "clustarr.io/variant"
	ArtworkMetaKeyLanguage       = "clustarr.io/language"
	ArtworkMetaKeyWidth          = "clustarr.io/width"
	ArtworkMetaKeyHeight         = "clustarr.io/height"
	ArtworkMetaKeyProfile        = "clustarr.io/profile"
	ArtworkMetaKeyOriginalDigest = "clustarr.io/original-digest"
	ArtworkMetaKeySource         = "clustarr.io/source"
	ArtworkMetaKeySourceURL      = "clustarr.io/source-url"
	ArtworkMetaKeyUpdatedAt      = "clustarr.io/updated-at"
	ArtworkMetaKeyRenderedFrom   = "clustarr.io/rendered-from"
	BucketMetaKeyKeyScheme       = "clustarr.io/key-scheme"
)

// ArtworkKeyScheme is BucketArtwork's key scheme, recorded in the bucket's
// metadata so a reader can check it (ArtworkKey builds the names).
const ArtworkKeyScheme = "<kind>/<uid>/<imageType>/<variant>"

// The metadata keys every object store shares (ADR-0021 §2): the version of
// its store's metadata set, what kind of object it is, and who wrote it.
// ArtworkMetaKeyVersion and ArtworkMetaKeyKind are the same keys under the
// artwork names.
const (
	ObjectMetaKeyVersion   = ArtworkMetaKeyVersion
	ObjectMetaKeyKind      = ArtworkMetaKeyKind
	ObjectMetaKeyWrittenBy = "clustarr.io/written-by"
)

// FingerprintMetaVersion is the version of the metadata set every
// fingerprint object carries (ADR-0021 §2), under ObjectMetaKeyVersion. A
// fingerprint written before it reads as the key absent, which the
// manager's maintenance audit backfills with SetMeta. Raise it when the set
// changes.
const FingerprintMetaVersion = "1"

// FingerprintMetaKind is a fingerprint object's ObjectMetaKeyKind.
const FingerprintMetaKind = "fingerprint"

// The metadata keys of a fingerprint object (ADR-0021 §2), built only by
// pkg/segments/fingerprintmeta.Build: the probe hash and window ("start" or
// "end") its name also carries, the FingerprintVersion it was computed
// under, and the analyzer's parameters -- the window's length in seconds,
// the sample rate and channels the audio was decoded to.
const (
	FingerprintMetaKeyProbeHash     = "clustarr.io/probe-hash"
	FingerprintMetaKeyWindow        = "clustarr.io/window"
	FingerprintMetaKeyVersion       = "clustarr.io/fingerprint-version"
	FingerprintMetaKeyWindowSeconds = "clustarr.io/window-seconds"
	FingerprintMetaKeySampleRate    = "clustarr.io/sample-rate"
	FingerprintMetaKeyChannels      = "clustarr.io/channels"
)

// FingerprintKeyScheme is ObjectStoreFingerprints' key scheme, recorded in
// the bucket's metadata (segments.FingerprintKey builds the names: no
// version suffix for the CLI decoder's legacy keys, ".v<N>" from version 2).
const FingerprintKeyScheme = "<probeHash>.<start|end>[.v<FingerprintVersion>]"

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
	RPCIndexSearch     = "clustarr.rpc.index.search"
	RPCIndexDownload   = "clustarr.rpc.index.download"
	RPCIndexQuery      = "clustarr.rpc.index.query"
	RPCMetadataLookup  = "clustarr.rpc.catalog.metadata.lookup"
	RPCMetadataSearch  = "clustarr.rpc.catalog.metadata.search"
	RPCMetadataResolve = "clustarr.rpc.catalog.metadata.resolve"
	RPCMetadataExtras  = "clustarr.rpc.catalog.metadata.extras"
	QueueGroupIndex    = "index"
	QueueGroupCatalog  = "catalog"
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
// the RSS fan-out subject index publishes parsed releases on.
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

// TranscodeEventSubject builds
// clustarr.evt.transcode.job.<action>.<mediafile-uid>, where the remediation
// loop publishes a file's transcode history (loop spec §5.16). The subject
// keeps its "job" token, so history consumers are unchanged (R7).
func TranscodeEventSubject(action, mediaFileUID string) string {
	return fmt.Sprintf("clustarr.evt.transcode.job.%s.%s", tok(action), tok(mediaFileUID))
}

// SubtitleEventSubject builds
// clustarr.evt.subtitle.subtitle.<action>.<mediafile-uid>.
func SubtitleEventSubject(action, mediaFileUID string) string {
	return fmt.Sprintf("clustarr.evt.subtitle.subtitle.%s.%s", tok(action), tok(mediaFileUID))
}

// WorkSearchSubject builds
// clustarr.work.catalog.search.<priority>.<mediaKey>.
func WorkSearchSubject(p Priority, mediaKey string) string {
	return fmt.Sprintf("clustarr.work.catalog.search.%s.%s", tok(string(p)), tok(mediaKey))
}

// WorkMetadataSubject builds
// clustarr.work.catalog.metadata.<high|normal>.<mediaKey>.
func WorkMetadataSubject(p Priority, mediaKey string) string {
	return fmt.Sprintf("clustarr.work.catalog.metadata.%s.%s", tok(string(p)), tok(mediaKey))
}

// WorkSegmentsPlanSubject builds
// clustarr.work.markers.plan.normal.<key>: plan one season's or
// movie's segment analysis (schema.SegmentsPlanTask).
func WorkSegmentsPlanSubject(key string) string {
	return "clustarr.work.markers.plan.normal." + tok(key)
}

// WorkSegmentsAnalyzeSubject builds
// clustarr.work.markers.analyze.normal.<key>.
func WorkSegmentsAnalyzeSubject(key string) string {
	return "clustarr.work.markers.analyze.normal." + tok(key)
}

// WorkMarkersSubject builds clustarr.work.markers.markers.normal.<mediaKey>:
// fetch one MediaFile's skip segments (schema.MarkersTask).
func WorkMarkersSubject(mediaKey string) string {
	return "clustarr.work.markers.markers.normal." + tok(mediaKey)
}

// MsgIDForMarkers is a markers task's Msg-Id, "markers/<file UID>/<seq>"
// (loop spec 2026-10-06 §4.12): the remediation loop's sequence, so a
// republish of one request is absorbed and the next request is not.
func MsgIDForMarkers(fileUID string, seq int64) string {
	return "markers/" + fileUID + "/" + strconv.FormatInt(seq, 10)
}

// MsgIDForMarkersAt is a deferred markers task's, republished for at:
// MsgIDForMarkers plus "/at-<unix>", so the dedup window keeps it.
func MsgIDForMarkersAt(fileUID string, seq int64, at time.Time) string {
	return MsgIDForMarkers(fileUID, seq) + "/at-" + strconv.FormatInt(at.Unix(), 10)
}

// WorkWantedScanSubject builds
// clustarr.work.catalog.wantedscan.low.<namespace>.
func WorkWantedScanSubject(namespace string) string {
	return "clustarr.work.catalog.wantedscan.low." + tok(namespace)
}

// The three import work subjects (amendment §A1.6). Unlike the catalog
// and caption work subjects they carry no priority token: the amendment
// spells them out as work.import.scan.<rootfolder>,
// work.import.list.<importlist> and work.import.fileimport.<download>,
// and there are no high/normal/low lanes to choose between -- one consumer
// drains each task type. They are still clustarr.work.<service>.<task>.<id>,
// five tokens, so [ScheduleSubject] accepts them.

// WorkScanSubject builds clustarr.work.import.scan.<rootfolder>. A scan of
// a large library is chunked by directory, so rootFolder here identifies the
// scan's root and the chunk rides in the payload, not the subject: a subject
// token per directory would make the work-queue stream's subject cardinality
// grow with the library.
func WorkScanSubject(rootFolder string) string {
	return "clustarr.work.import.scan." + tok(rootFolder)
}

// WorkListSubject builds clustarr.work.import.list.<importlist>.
func WorkListSubject(importList string) string {
	return "clustarr.work.import.list." + tok(importList)
}

// WorkFileImportSubject builds
// clustarr.work.import.fileimport.<download>.
func WorkFileImportSubject(downloadUID string) string {
	return "clustarr.work.import.fileimport." + tok(downloadUID)
}

// SubjectImportRecycleSweep is the one subject the recycle sweep task rides.
// It carries no object token: one task sweeps every bin, each once, with the
// longest retention any RootFolder sharing it asks for
// (fileimport.RecycleSweeper.SweepOnce), which a per-RootFolder task could
// not know.
const SubjectImportRecycleSweep = "clustarr.work.import.recycle.sweep"

// WorkRSSSubject builds clustarr.work.index.rss.normal.<indexer-uid>.
func WorkRSSSubject(indexerUID string) string {
	return "clustarr.work.index.rss.normal." + tok(indexerUID)
}

// WorkFetchSubject builds
// clustarr.work.caption.fetch.<priority>.<mediafile-uid>.<langKey>.
func WorkFetchSubject(p Priority, fileUID, langKey string) string {
	return fmt.Sprintf("clustarr.work.caption.fetch.%s.%s.%s",
		tok(string(p)), tok(fileUID), tok(langKey))
}

// WorkFetchSubjectAnyPriority is fileUID's langKey fetch subject with a
// wildcard for the lane, for PurgeSubject when the loop withdraws a request
// (loop spec §3.8): a forced search and a scheduled one go to different lanes.
func WorkFetchSubjectAnyPriority(fileUID, langKey string) string {
	return fmt.Sprintf("clustarr.work.caption.fetch.*.%s.%s", tok(fileUID), tok(langKey))
}

// WorkArtworkFetchSubject builds
// clustarr.work.catalog.artwork.fetch.<mediaKey>, consumed by the
// metadata gateway's ConsumerCatalogArtworkFetch (spec §B.7).
func WorkArtworkFetchSubject(mediaKey string) string {
	return "clustarr.work.catalog.artwork.fetch." + tok(mediaKey)
}

// WorkArtworkRenderSubject builds
// clustarr.work.catalog.artwork.render.<mediaKey>, consumed by the
// renderer role's ConsumerCatalogArtworkRender (spec §C.6).
func WorkArtworkRenderSubject(mediaKey string) string {
	return "clustarr.work.catalog.artwork.render." + tok(mediaKey)
}

// ArtworkKey builds the object name for one artwork object in
// events.BucketArtwork: "<kind>/<uid>/<imageType>/<variant>", e.g.
// "movie/8b2c.../poster/original" (spec §B.2).
//
// It panics on an empty part or a part containing "/": the built name uses
// "/" as its own segment separator, so a caller-supplied "/" would forge a
// segment boundary the same way an unescaped "." would inside a subject
// token (see MediaKey's doc comment). Every part -- the media kind, the
// object's UID, the CRD's ImageType enum and ArtworkVariantOriginal/Overlay
// -- is a value the caller controls, not user input, so a panic here is a
// caller bug, not a runtime condition to recover from.
func ArtworkKey(kind commonv1.MediaKind, uid types.UID, imageType, variant string) string {
	parts := []string{string(kind), string(uid), imageType, variant}
	for _, p := range parts {
		if p == "" {
			panic("events: ArtworkKey: empty part")
		}
		if strings.Contains(p, "/") {
			panic(fmt.Sprintf("events: ArtworkKey: part %q contains \"/\"", p))
		}
	}
	return strings.Join(parts, "/")
}

// ScheduleSubject returns the holding subject for a scheduled publish to
// target. A scheduled message is stored on the holding subject and the broker
// republishes it to target when the schedule fires, so the two must differ or
// the message would re-trigger itself.
//
// The holding subject inserts a "sched" token after the service, turning
// clustarr.work.catalog.search.normal.<key> into
// clustarr.work.catalog.sched.search.normal.<key>. It therefore stays inside
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

// DLQSubject builds clustarr.dlq.<service>.<task>.<id>.
func DLQSubject(service, task, id string) string {
	return fmt.Sprintf("clustarr.dlq.%s.%s.%s", tok(service), tok(task), tok(id))
}

// ExclusionKey is the clustarr-import-exclusions key for one exclusion. The
// import-list and search paths consult this bucket rather than listing
// ImportExclusion resources on every candidate, which would not scale.
//
// Both segments go through [KVKeyToken], and that is not a precaution: id comes straight out of ImportExclusion.spec.externalIDs, an
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

// MsgIDForSubtitleFetch is a subtitle fetch task's deduplication ID,
// "subtitle/<fileUID>/<langKey>/<seq>" (loop spec §6.5): every dispatch
// carries the sequence the loop issued, so it is unique for the life of the
// file and the one-hour window absorbs only a republish of that dispatch.
func MsgIDForSubtitleFetch(fileUID, langKey string, seq int64) string {
	return fmt.Sprintf("subtitle/%s/%s/%d", fileUID, langKey, seq)
}

// TranscodeLeaseTTL is how long a transcode task lease lives after its
// holder's last renewal. It is the lease bucket's TTL, so the server, not a
// worker's clock, decides when a lease has lapsed.
const TranscodeLeaseTTL = 90 * time.Second

// WorkTranscodeTaskSubject is where the remediation loop publishes one
// MediaFile's transcode dispatch for the pool of its profile and hardware
// class (loop spec §4.12): the profile's UID, the class, and the MediaFile's
// UID. Profile and file are UIDs because a profile name may contain ".".
func WorkTranscodeTaskSubject(profileUID, class, mediaFileUID string) string {
	return fmt.Sprintf("clustarr.work.transcode.task.%s.%s.%s", tok(profileUID), tok(class), tok(mediaFileUID))
}

// WorkTranscodeTaskSubjectAnyPool is a MediaFile's task subject with
// wildcards in place of both the profile and the class token: every task of
// the file, on every pool it could have been dispatched to (a file has at
// most one live seq). Withdrawal purges with it (loop spec §5.10), and the
// lost-worker check asks whether a task is still stored with it (§5.9). It
// is for PurgeSubject and Subjects.
func WorkTranscodeTaskSubjectAnyPool(mediaFileUID string) string {
	return "clustarr.work.transcode.task.*.*." + tok(mediaFileUID)
}

// FilterTranscodeTasks is one pool's share of CLUSTARR_WORK_TRANSCODE.
func FilterTranscodeTasks(profileUID, class string) string {
	return fmt.Sprintf("clustarr.work.transcode.task.%s.%s.>", tok(profileUID), tok(class))
}

// TranscodePoolDurablePrefix starts every name TranscodeTaskConsumerName
// builds; the transcode controller's durable sweep matches on it.
const TranscodePoolDurablePrefix = "transcode-pool-"

// TranscodeTaskConsumerName is the durable one pool's workers share.
func TranscodeTaskConsumerName(profileUID, class string) string {
	return TranscodePoolDurablePrefix + KVKeyToken(profileUID) + "-" + KVKeyToken(class)
}

// TranscodeLeaseKey is a MediaFile's transcode lease in
// clustarr-transcode-leases (loop spec §4.12).
func TranscodeLeaseKey(mediaFileUID string) string { return "lease." + KVKeyToken(mediaFileUID) }

// MsgIDForTranscodeTask is a transcode dispatch's Msg-Id,
// "transcode/<mfUID>/<seq>" (loop spec §5.3): a republish of one seq inside
// the stream's Duplicates window is absorbed, and the next seq never is.
func MsgIDForTranscodeTask(mfUID string, seq int64) string {
	return "transcode/" + mfUID + "/" + strconv.FormatInt(seq, 10)
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
