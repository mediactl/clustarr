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

// SubtitleEventType is the Clustarr-Type header of every subtitle event.
const SubtitleEventType = "subtitle.SubtitleEvent"

// SubtitleEvent reports a subtitle being downloaded, upgraded or failing
// (loop spec §6.7.4). Subject:
// clustarr.evt.subtitle.subtitle.<downloaded|upgraded|failed>.<mediafile-uid>.
// It is published only after the answer landed in clustarr-subtitles.
type SubtitleEvent struct {
	// MediaFile is the file the subtitle is for.
	MediaFile Ref `json:"mediaFile"`

	// Action is one of downloaded, upgraded or failed.
	Action string `json:"action"`

	// LangKey is the language key, e.g. "en", "en:forced" or "en:hi".
	LangKey string `json:"langKey"`

	// Seq is the dispatch's sequence the answer was for.
	Seq int64 `json:"seq"`

	// Provider is the subtitle provider that served the file.
	Provider string `json:"provider,omitempty"`

	// ProviderID is the provider-scoped subtitle identifier.
	ProviderID string `json:"providerID,omitempty"`

	// Score is the match score awarded to the chosen candidate.
	Score int32 `json:"score,omitempty"`

	// PreviousScore is the score of the subtitle that was replaced, for an
	// upgrade.
	PreviousScore int32 `json:"previousScore,omitempty"`

	// Path is where the sidecar was written.
	Path string `json:"path,omitempty"`

	// Reason explains a failure.
	Reason string `json:"reason,omitempty"`

	// At is when the transition happened.
	At time.Time `json:"at"`
}

// Schema implements Payload.
func (SubtitleEvent) Schema() string { return "subtitle.SubtitleEvent.v2" }

// FetchTaskType is the Clustarr-Type header of every fetch task.
const FetchTaskType = "subtitle.FetchTask"

// FetchTask asks a fetch worker to look for one language of one MediaFile
// (loop spec §6.7.1). Subject:
// clustarr.work.caption.fetch.<priority>.<mediafile-uid>.<langKey>. It
// carries everything item-specific, so the worker reads no MediaFile,
// Movie, Episode, Series or RootFolder; Seq fences it against the file's
// record in clustarr-subtitles.
type FetchTask struct {
	// MediaFile is the file the loop planned: namespace, name and UID.
	MediaFile Ref `json:"mediaFile"`
	// Path is the logical /data path the plan was made against.
	Path      string `json:"path"`
	ProbeHash string `json:"probeHash"`
	// LangKey is the language key to fetch, e.g. "en" or "en:forced".
	LangKey string `json:"langKey"`
	// Seq is the sequence the loop issued for this dispatch.
	Seq               int64  `json:"seq"`
	Profile           string `json:"profile"`
	ProfileGeneration int64  `json:"profileGeneration"`
	// MinScore is the absolute threshold, from the loop (§6.4.5).
	MinScore int32 `json:"minScore"`
	// UpgradeMarginPoints is the profile's upgrade.minDeltaPoints.
	UpgradeMarginPoints int32 `json:"upgradeMarginPoints"`
	Upgrade             bool  `json:"upgrade,omitempty"`
	Forced              bool  `json:"forced,omitempty"`
	// OnDisk is the subtitle already on disk for this language, which only
	// a better one replaces.
	OnDisk *OnDiskSubtitle `json:"onDisk,omitempty"`
	// SidecarMode is the RootFolder's fileMode; 0 means 0664.
	SidecarMode uint32        `json:"sidecarMode,omitempty"`
	Query       SubtitleQuery `json:"query"`
	// Extract is the embedded streams the local tier writes out
	// (extractableStreams), the exact set the planner computed.
	Extract []commonv1.SubtitleStream `json:"extract,omitempty"`
}

// Schema implements Payload.
func (FetchTask) Schema() string { return "subtitle.FetchTask.v2" }

// SubtitleQuery is what identifies the item to the providers.
type SubtitleQuery struct {
	Kind commonv1.MediaKind `json:"kind"`
	// IDs uses pkg/subtitles.Query's keys: tmdb, imdb, tvdb, parent_imdb,
	// parent_tmdb.
	IDs     map[string]string `json:"ids,omitempty"`
	Title   string            `json:"title,omitempty"`
	Year    int32             `json:"year,omitempty"`
	Season  int32             `json:"season,omitempty"`
	Episode int32             `json:"episode,omitempty"`
	// ReleaseTitle is spec.importedFrom.releaseTitle, else the file stem.
	ReleaseTitle string `json:"releaseTitle,omitempty"`
}

// OnDiskSubtitle is the sidecar a fetch would replace: its base name and
// the score it was downloaded at.
type OnDiskSubtitle struct {
	Name  string `json:"name"`
	Score int32  `json:"score"`
}

// SubtitleVerdict is what a fetch found (loop spec §6.6).
type SubtitleVerdict string

// Subtitle verdicts.
const (
	SubtitleVerdictDownloaded  SubtitleVerdict = "downloaded"
	SubtitleVerdictKept        SubtitleVerdict = "kept"
	SubtitleVerdictUnavailable SubtitleVerdict = "unavailable"
	SubtitleVerdictFailed      SubtitleVerdict = "failed"
	SubtitleVerdictThrottled   SubtitleVerdict = "throttled"
	SubtitleVerdictDropped     SubtitleVerdict = "dropped"
)

// SubtitleRecordSchema is RecordHeader.Schema on every subtitle record:
// "records.<remediation>.v1" (loop spec §4.5).
const SubtitleRecordSchema = "records.subtitle.v1"

// MaxSubtitleLastError is SubtitleItemStatus.lastError's MaxLength.
const MaxSubtitleLastError = 512

// SubtitleRecord is one language's record in clustarr-subtitles, key
// events.RecordSubKey(fileUID, langKey) (loop spec §6.6). The loop writes
// requested and withdrawn; the caption domain writes answered and failed.
type SubtitleRecord struct {
	RecordHeader
	ProbeHash         string          `json:"probeHash"`
	ProfileGeneration int64           `json:"profileGeneration"`
	Verdict           SubtitleVerdict `json:"verdict,omitempty"`
	Score             int32           `json:"score,omitempty"`
	ScoreOutOf        int32           `json:"scoreOutOf,omitempty"`
	Provider          string          `json:"provider,omitempty"`     // at most 253 bytes
	SubtitleID        string          `json:"subtitleID,omitempty"`   // at most 256 bytes
	Name              string          `json:"name,omitempty"`         // a base name, at most 255 bytes
	ReplacedName      string          `json:"replacedName,omitempty"` // the sidecar an upgrade removed
	LastError         string          `json:"lastError,omitempty"`    // at most MaxSubtitleLastError
	ThrottledUntil    *time.Time      `json:"throttledUntil,omitempty"`
}
