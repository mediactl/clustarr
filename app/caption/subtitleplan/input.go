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

package subtitleplan

import (
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/providerset"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/subtitlestore"
)

// Reasons a block is Blocked (status.subtitles.reason, §6.4.1).
const (
	ReasonItemNotFound       = "ItemNotFound"
	ReasonNoProfile          = "NoProfile"
	ReasonProfileInvalid     = "ProfileInvalid"
	ReasonNotOnDataVolume    = "MediaFileNotOnDataVolume"
	ReasonMediaDirUnreadable = "MediaDirUnreadable"
	ReasonNotOnDisk          = "MediaFileNotOnDisk"
)

// RequestTimeout is subtitleRequestTimeout (§6.4.5): the fetch durables'
// worst span, seven expired deadlines plus the eighth delivery's, is 25 h
// 12 m, so a dispatch with no answer after 26 h is closed.
const RequestTimeout = 26 * time.Hour

const (
	// diskRetry is the wake of a disk block (§6.4.1).
	diskRetry = 5 * time.Minute
	// backlogRetry is the wake of a due item the backlog gate deferred.
	backlogRetry = 5 * time.Minute
	// minWake floors every wake (reconciler.go's 30 s).
	minWake = 30 * time.Second
	// maxSidecars is status.sidecars' MaxItems; maxItems is
	// status.subtitles.items' and wanted's.
	maxSidecars = 32
	maxItems    = 20
)

// Priority is a dispatch's lane: the values of events.Priority, which the
// adapter converts (this package imports no bus).
type Priority string

// Lanes (§6.4.5): high when forced, low for an upgrade, normal otherwise.
const (
	PriorityHigh   Priority = "high"
	PriorityNormal Priority = "normal"
	PriorityLow    Priority = "low"
)

// Item is what the planner needs of the file's Movie or Episode (and its
// Series): whether it exists, its original language and the providers'
// query.
type Item struct {
	Exists           bool
	OriginalLanguage string
	Query            schema.SubtitleQuery
}

// Listing is one ReadDir of the media directory: the regular files' names,
// or the error that read failed with.
type Listing struct {
	Names []string
	Err   error
}

// Listed is what this process last listed a file for: its path and probe
// hash (gates 3 and 4 of §6.4.3).
type Listed struct {
	Path, ProbeHash string
}

// Input is everything one plan reads (§6.4).
type Input struct {
	// File is the pass's view, its status the draft the planners before
	// subtitles left.
	File *catalogv1alpha1.MediaFile
	// Labels is View.Labels(): what every profile selection reads (§3.3).
	Labels map[string]string
	// ProbeCurrent is View.ProbeCurrent() on the draft (§3.5).
	ProbeCurrent bool
	Item         Item
	// Profiles is every SubtitleProfile (cluster-scoped, from the cache,
	// not deep-copied).
	Profiles []subtitlev1alpha1.SubtitleProfile
	// Extractors is the enabled embedded SubtitleProviders in the file's
	// namespace (EmbeddedEntries), unordered.
	Extractors []providerset.Entry
	// RootFolders is the namespace's, for the sidecar file mode.
	RootFolders []catalogv1alpha1.RootFolder
	// Records is the records read this pass, by langKey.
	Records map[string]subtitlestore.Entry
	// Listing is nil when Needs said not to list.
	Listing *Listing
	// LastListed is this process's last listing of the file; nil when never.
	LastListed *Listed
	// ForceNonce is subtitle.clustarr.io/search's value (§6.5).
	ForceNonce string
	// BacklogOpen is §6.4.6's gate.
	BacklogOpen bool
	// PacerSlot is the file's records-pacer reservation; zero when none was
	// asked (§4.7).
	PacerSlot time.Time
	// Queued is the langKeys whose fetch subject still holds a stored task;
	// nil when that was not read (§6.4.6).
	Queued  map[string]bool
	DataDir string
	Now     time.Time
}

// Need is what Gather must read before Plan can decide: the records by
// langKey, the pacer reservation, and whether to list the directory.
type Need struct {
	Records []string
	Reserve bool
	List    bool
}

// Dispatch is one request the adapter turns into a record write and a
// publish.
type Dispatch struct {
	LangKey string
	Seq     int64
	// Record: write the requested record at Revision (0 creates), refusing
	// to replace an answer above IncorporatedThrough.
	Record              bool
	Revision            uint64
	IncorporatedThrough int64
	// New: a new dispatch this pass, which consumes the pacer reservation.
	New bool
	// Republish: the record is requested at Seq already; publish only.
	Republish bool
	// Evicted: Gather's stored-task reading says the task is gone, so the
	// republish takes a fresh Msg-Id (§6.4.6).
	Evicted  bool
	Priority Priority
	Task     schema.FetchTask
}

// Withdraw closes LangKey's dispatch at Seq. The adapter's Withdrawer reads
// the record afresh, so no revision travels.
type Withdraw struct {
	LangKey string
	Seq     int64
}

// Event is one Kubernetes Event the adapter records on the file.
type Event struct {
	Type, Reason, Message string
}

// Decision is what one plan decided.
type Decision struct {
	// Block is status.subtitles; nil means no block.
	Block *catalogv1alpha1.SubtitlesStatus
	// Sidecars is status.sidecars: from this pass's listing when
	// SidecarsListed, else carried from the stored status.
	Sidecars       []catalogv1alpha1.Sidecar
	SidecarsListed bool
	// HandledNonce is status.handledNonces.subtitleSearch.
	HandledNonce string
	Dispatches   []Dispatch
	Withdraws    []Withdraw
	// Wake is when the file next needs a pass; zero is none.
	Wake time.Time
	// Held: the probe is pending, and the block, sidecars and nonce stay
	// verbatim (§6.4.1).
	Held    bool
	Events  []Event
	FileUID string
}
