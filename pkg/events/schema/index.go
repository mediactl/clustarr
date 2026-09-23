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

package schema

import (
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// MaxSearchReleases caps how many releases a single SearchResponse may carry.
// indexarr truncates beyond this, reporting the truncation per indexer.
const MaxSearchReleases = 500

// Release is one parsed indexer release fanned out on
// clustarr.rel.<protocol>.<indexerName>.<newznabTop>.
type Release struct {
	// Info is the release as the indexer reported it, already normalised.
	// A release a federated search merged from several indexers carries the
	// others in Info.AlsoOn (wire key info.alsoOn): the provenance lives on
	// ReleaseInfo, not here, so it survives into Search.status.results and a
	// Download's spec.release without a second copy to keep in step. It is
	// additive and optional, so the payload keeps its schema version.
	Info commonv1.ReleaseInfo `json:"info"`

	// ParsedTitle is the cleaned title the parser extracted.
	ParsedTitle string `json:"parsedTitle,omitempty"`

	// Year is the year parsed from the title.
	Year int32 `json:"year,omitempty"`

	// Seasons lists the season numbers the release covers.
	Seasons []int32 `json:"seasons,omitempty"`

	// Episodes lists the episode numbers the release covers.
	Episodes []int32 `json:"episodes,omitempty"`

	// Absolute lists absolute episode numbers, for anime.
	Absolute []int32 `json:"absolute,omitempty"`

	// AirDate is the air date parsed from a daily-series title.
	AirDate *time.Time `json:"airDate,omitempty"`

	// FullSeason marks a complete season pack.
	FullSeason bool `json:"fullSeason,omitempty"`

	// MultiSeason marks a pack spanning more than one season.
	MultiSeason bool `json:"multiSeason,omitempty"`

	// Special marks a special or extra.
	Special bool `json:"special,omitempty"`

	// Kind is the media kind the classifier assigned to the title.
	Kind commonv1.MediaKind `json:"kind,omitempty"`

	// Hints carries the codec, HDR, audio and container tokens the parser
	// recognised, keyed by hint name.
	Hints map[string][]string `json:"hints,omitempty"`

	// FetchedAt is when indexarr read the release from the indexer.
	FetchedAt time.Time `json:"fetchedAt"`
}

// Schema implements Payload.
func (Release) Schema() string { return "index.Release.v1" }

// IndexerEvent reports an indexer being disabled, recovered or rate limited.
// Subject: clustarr.evt.index.indexer.<disabled|recovered|limited>.<uid>.
type IndexerEvent struct {
	// IndexerRef is the Indexer the event is about.
	IndexerRef Ref `json:"indexerRef"`

	// Action is one of disabled, recovered or limited.
	Action string `json:"action"`

	// Reason explains the transition.
	Reason string `json:"reason,omitempty"`

	// Until is when a disable or limit expires.
	Until *time.Time `json:"until,omitempty"`

	// Failures is the consecutive failure count that triggered a disable.
	Failures int32 `json:"failures,omitempty"`

	// At is when the transition happened.
	At time.Time `json:"at"`
}

// Schema implements Payload.
func (IndexerEvent) Schema() string { return "index.IndexerEvent.v1" }

// RssTask asks an RSS worker to poll one indexer. It is published with a
// schedule matching the indexer's RSS interval.
// Subject: clustarr.work.indexarr.rss.normal.<indexer-uid>.
type RssTask struct {
	// IndexerRef is the Indexer to poll.
	IndexerRef Ref `json:"indexerRef"`

	// Categories limits the poll to these Newznab category IDs.
	Categories []int32 `json:"categories,omitempty"`

	// Since is the publish time of the newest release already seen, so the
	// worker can stop paging early.
	Since *time.Time `json:"since,omitempty"`
}

// Schema implements Payload.
func (RssTask) Schema() string { return "index.RssTask.v1" }

// DefinitionsSync asks a worker to refresh the Cardigann definition set.
// Subject: clustarr.work.indexarr.definitions.normal.sync.
type DefinitionsSync struct {
	// Source is the definitions repository or bundle to sync from. Empty
	// means the built-in bundle.
	Source string `json:"source,omitempty"`

	// Revision is the definition-set revision to move to. Empty means latest.
	Revision string `json:"revision,omitempty"`

	// Force re-applies definitions even when the revision is unchanged.
	Force bool `json:"force,omitempty"`
}

// Schema implements Payload.
func (DefinitionsSync) Schema() string { return "index.DefinitionsSync.v1" }

// SearchOutcomeStatus is how one indexer fared in a federated search.
type SearchOutcomeStatus string

// Search outcome statuses.
const (
	// SearchOutcomeOK means the indexer answered within the deadline.
	SearchOutcomeOK SearchOutcomeStatus = "ok"

	// SearchOutcomeTimeout means the indexer was still running when the
	// single reply had to be sent.
	SearchOutcomeTimeout SearchOutcomeStatus = "timeout"

	// SearchOutcomeError means the indexer failed.
	SearchOutcomeError SearchOutcomeStatus = "error"

	// SearchOutcomeSkipped means the indexer was disabled, rate limited or
	// did not support the query.
	SearchOutcomeSkipped SearchOutcomeStatus = "skipped"
)

// SearchQueryMode names which parameter set a per-indexer query used.
type SearchQueryMode string

// Search query modes.
const (
	// SearchQueryModeID means at least one of the request's id parameters
	// (imdbid/tmdbid/tvdbid) matched what the indexer advertises, and the
	// query was built from ids.
	SearchQueryModeID SearchQueryMode = "id"

	// SearchQueryModeText means the indexer advertised none of the
	// request's id parameters, so the query fell back to a free-text
	// search built from SearchRequest.Text (spec §6.2's "t=search&q=").
	SearchQueryModeText SearchQueryMode = "text"
)

// SearchOutcome reports how one indexer fared in a federated search.
type SearchOutcome struct {
	// IndexerRef is the indexer this outcome is about.
	IndexerRef Ref `json:"indexerRef"`

	// IndexerName is the indexer display name.
	IndexerName string `json:"indexerName,omitempty"`

	// Status is how the indexer fared.
	Status SearchOutcomeStatus `json:"status"`

	// Releases is how many releases this indexer contributed.
	Releases int32 `json:"releases"`

	// ElapsedMillis is how long the indexer took, in milliseconds.
	ElapsedMillis int64 `json:"elapsedMillis,omitempty"`

	// Error is the failure message for an error outcome.
	Error string `json:"error,omitempty"`

	// QueryMode names which parameter set the query actually used: ids,
	// preferred wherever they work, or a title-and-year text fallback when
	// the indexer supported none of the request's id parameters. Empty for
	// an outcome that never reached query construction (skipped, or still
	// running when the reply had to be sent).
	QueryMode SearchQueryMode `json:"queryMode,omitempty"`
}

// SearchRequest is the request half of the federated search RPC.
// Subject: clustarr.rpc.indexarr.search.
type SearchRequest struct {
	// Namespace scopes the search to one namespace's Indexers.
	//
	// It is optional so an older producer can omit it, but indexarr has no
	// other way to learn it: IndexerRefs carries a namespace and catalogarr
	// populates that only for interactive searches, so an automatic search
	// would otherwise arrive with no namespace at all and indexarr would
	// have to list Indexers cluster-wide -- serving one namespace's media
	// from another's indexer, with that indexer's credentials, counted
	// against its grab limit. When empty, indexarr falls back to
	// cluster-wide AND logs a warning naming the request, so the old
	// behaviour is observable rather than silent.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Kind is the media kind being searched for.
	Kind commonv1.MediaKind `json:"kind"`

	// Text is the free-text query. Ids are preferred over it wherever an
	// indexer supports one (see indexarr/search's buildQuery): an id-based
	// match is server-side and exact, while a text query is only as precise
	// as the indexer's own keyword search. catalogarr/worker/search.
	// BuildSearchRequest sets this from the item's resolved title (and year,
	// or SxxEyy for an episode) so an automatic search still reaches an
	// indexer that advertises no id parameter at all, matching the Torznab
	// facade and an interactive Search, which already set it from their own
	// free-text input.
	Text string `json:"text,omitempty"`

	// IDs are the external IDs to search by, preferred over Text.
	IDs map[string]string `json:"ids,omitempty"`

	// Season narrows a series search.
	Season *int32 `json:"season,omitempty"`

	// Episode narrows a series search.
	Episode *int32 `json:"episode,omitempty"`

	// Year narrows a movie search.
	Year int32 `json:"year,omitempty"`

	// Categories limits the search to these Newznab category IDs.
	Categories []int32 `json:"categories,omitempty"`

	// IndexerRefs limits the search to these indexers. Empty means every
	// enabled indexer that supports the query.
	IndexerRefs []Ref `json:"indexerRefs,omitempty"`

	// Protocols limits the search to these transfer protocols.
	Protocols []commonv1.Protocol `json:"protocols,omitempty"`

	// Limit caps the number of releases in the reply. It is clamped to
	// MaxSearchReleases.
	Limit int32 `json:"limit,omitempty"`

	// DeadlineMillis is how long the caller will wait. indexarr replies once
	// at min(deadline, 45s).
	DeadlineMillis int64 `json:"deadlineMillis,omitempty"`

	// UserInvoked marks an interactive search.
	UserInvoked bool `json:"userInvoked,omitempty"`
}

// Schema implements Payload.
func (SearchRequest) Schema() string { return "index.SearchRequest.v1" }

// SearchResponse is the single reply to a federated search.
type SearchResponse struct {
	// Releases holds the merged results, capped at MaxSearchReleases.
	Releases []Release `json:"releases,omitempty"`

	// Outcomes reports how each indexer fared, including the ones that were
	// still running when the reply was sent.
	Outcomes []SearchOutcome `json:"outcomes,omitempty"`

	// Truncated says the result set was cut at MaxSearchReleases.
	Truncated bool `json:"truncated,omitempty"`
}

// Schema implements Payload.
func (SearchResponse) Schema() string { return "index.SearchResponse.v1" }

// DownloadRequest asks indexarr to fetch a release payload using its own
// session cookies and passkeys. Subject: clustarr.rpc.indexarr.download.
type DownloadRequest struct {
	// IndexerRef is the indexer holding the session to use.
	IndexerRef Ref `json:"indexerRef"`

	// GUID is the indexer-scoped release identifier.
	GUID string `json:"guid"`

	// URL is the download link from the release.
	URL string `json:"url,omitempty"`
}

// Schema implements Payload.
func (DownloadRequest) Schema() string { return "index.DownloadRequest.v1" }

// DownloadResponse carries exactly one of Bytes, MagnetURL or RedirectURL.
type DownloadResponse struct {
	// Bytes is the .torrent or .nzb file.
	Bytes []byte `json:"bytes,omitempty"`

	// MagnetURL is the magnet link, when the indexer returned one instead.
	MagnetURL string `json:"magnetURL,omitempty"`

	// RedirectURL is where to fetch the payload, when the indexer will not
	// proxy it.
	RedirectURL string `json:"redirectURL,omitempty"`

	// ContentType is the MIME type of Bytes.
	ContentType string `json:"contentType,omitempty"`

	// Error is the failure message when the fetch failed.
	Error string `json:"error,omitempty"`
}

// Schema implements Payload.
func (DownloadResponse) Schema() string { return "index.DownloadResponse.v1" }

// QueryRequest is a direct query against the indexarr release index, used by
// Search custom resources in query mode and by the Torznab facade.
// Subject: clustarr.rpc.indexarr.query.
type QueryRequest struct {
	// Text is the full-text query.
	Text string `json:"text,omitempty"`

	// Filters are field filters such as protocol, indexer or quality.
	Filters map[string]string `json:"filters,omitempty"`

	// Limit caps the number of releases returned.
	Limit int32 `json:"limit,omitempty"`

	// Offset pages through the result set.
	Offset int32 `json:"offset,omitempty"`
}

// Schema implements Payload.
func (QueryRequest) Schema() string { return "index.QueryRequest.v1" }

// QueryResponse is the reply to a release-index query.
type QueryResponse struct {
	// Releases holds the matching releases.
	Releases []Release `json:"releases,omitempty"`

	// Total is how many releases matched before paging.
	Total int64 `json:"total,omitempty"`

	// Error is the failure message when the query failed.
	Error string `json:"error,omitempty"`
}

// Schema implements Payload.
func (QueryResponse) Schema() string { return "index.QueryResponse.v1" }
