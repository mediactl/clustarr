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

package rescan

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// UnmatchedFile is one file the walk could not attribute to a catalog item.
// It mirrors catalogv1alpha1.UnmatchedFile without importing the CRD
// package: a Progress value travels through a KV entry rather than the
// apiserver, and keeps the same "plain data only" discipline bus payloads
// use.
//
// Path is relative to the RootFolder being walked, matching the CRD field's
// own contract ("the file path relative to the root folder").
type UnmatchedFile struct {
	// Path is the file, relative to the root folder that was walked.
	Path string `json:"path"`

	// Reason explains, in words a user can act on, why the file could not
	// be attributed.
	Reason string `json:"reason"`

	// Candidates lists the catalog items the scanner considered but could
	// not choose between. Capped at [MaxCandidates] by the caller, which is
	// the CRD's own MaxItems.
	Candidates []string `json:"candidates,omitempty"`

	// SeenAt is when the walk observed the file.
	SeenAt time.Time `json:"seenAt"`
}

// Progress is the worker's running -- and, once Done, final -- tally for one
// LibraryScan. The worker checkpoints it to a clustarr-progress key roughly
// every [checkpointInterval]; the LibraryScan controller polls that key and
// aggregates it into status, because the controller is the single writer of
// LibraryScan.status and the single-writer rule has no worker exception
// here.
//
// # What the counters mean
//
// Every regular file the walk visits lands in exactly one place:
//
//   - a media file of the root folder's kind (or a suspected sample) is
//     SEEN, and then matched (FilesMatched), skipped (FilesSkipped, and one
//     of Unchanged, Transcoded, TranscodeOutputs or Deferred says why),
//     awaiting the episodes of its series (AwaitingEpisodes) or unmatched
//     (an entry in Unmatched, which is capped, so the count is not
//     derivable from the list);
//   - anything else is NOT CONSIDERED, and one of NotMedia, Parts, Extras
//     or Samples counts it. Those never reach LibraryScan.status's
//     counters: they are not media, so they are neither matched nor
//     skipped media -- which is what FilesSkipped used to conflate them
//     with, alongside unchanged and transcoded files;
//   - an entry the walk could not read is counted in Unreadable and listed
//     in Unmatched with [CodeUnreadable].
//
// LibraryScanStatus has one skip counter and no field for the rest, so the
// controller writes FilesSkipped (the CRD's own "an incremental scan
// skipping an unchanged fingerprint") and renders the whole breakdown,
// [Progress.Summary], into the Ready condition's message.
type Progress struct {
	// Done is true once the walk has finished, successfully or not. Until
	// then the controller keeps the scan in the Running phase.
	Done bool `json:"done"`

	// Error is non-empty when the walk failed; the controller turns it into
	// the Failed phase and a False Ready condition.
	Error string `json:"error,omitempty"`

	// FilesSeen counts files classified as media (a suspected sample
	// included) and therefore considered for attribution.
	FilesSeen int64 `json:"filesSeen"`

	// FilesMatched counts files attributed to a catalog item and recorded:
	// a MediaFile created or refreshed, or a post-transcode file's change
	// handed to catalogarr (HandedOver).
	FilesMatched int64 `json:"filesMatched"`

	// ItemsCreated counts catalog items the walk created.
	ItemsCreated int64 `json:"itemsCreated"`

	// ItemsUpdated counts catalog items the walk updated.
	ItemsUpdated int64 `json:"itemsUpdated"`

	// FilesSkipped counts media files already in the catalog that the walk
	// deliberately wrote nothing for: Unchanged + Transcoded +
	// TranscodeOutputs + Deferred.
	FilesSkipped int64 `json:"filesSkipped"`

	// Unchanged counts files an incremental scan found at the size and
	// mtime their MediaFile records.
	Unchanged int64 `json:"unchanged,omitempty"`

	// Transcoded counts post-transcode files (spec.original false, so
	// catalogarr owns their fingerprint) found unchanged on disk.
	Transcoded int64 `json:"transcoded,omitempty"`

	// Deferred counts files whose MediaFile changed between the walk
	// reading it and writing it, twice over; the next scan picks them up.
	Deferred int64 `json:"deferred,omitempty"`

	// TranscodeOutputs counts files squasharr wrote that no MediaFile
	// records: a container change's new file, named by its Succeeded
	// TranscodeJob, before catalogarr moves spec.path to it; or a
	// replaceSource=false output kept beside its source, named by its job
	// or, once the job is gone, recognised by its name and tag (keptOutput).
	// They belong to the item whose file was transcoded; adopting one would
	// make a duplicate or an unmatched entry.
	TranscodeOutputs int64 `json:"transcodeOutputs,omitempty"`

	// HandedOver counts post-transcode files whose bytes changed on disk:
	// the walk told catalogarr (AnnotationObservedFingerprint) instead of
	// writing the fields it owns. Also counted in FilesMatched.
	HandedOver int64 `json:"handedOver,omitempty"`

	// AwaitingEpisodes counts episode files of a series that has no
	// episodes yet -- typically one this walk created from its folder's
	// TheTVDB id, whose episode list the Series controller fans out only
	// once its metadata arrives. They are attributable, just not yet, so
	// they are counted rather than listed in the capped Unmatched, where
	// thousands of them would evict the files that need a person; a later
	// scan attributes them.
	AwaitingEpisodes int64 `json:"awaitingEpisodes,omitempty"`

	// NotMedia, Parts, Extras and Samples count the files the walk did not
	// consider at all: a non-media extension, a partial download, a file in
	// a video extras folder, and a file whose name marks it a sample.
	NotMedia int64 `json:"notMedia,omitempty"`
	Parts    int64 `json:"parts,omitempty"`
	Extras   int64 `json:"extras,omitempty"`
	Samples  int64 `json:"samples,omitempty"`

	// Unreadable counts entries the walk could not read (each is also in
	// Unmatched, with [CodeUnreadable]).
	Unreadable int64 `json:"unreadable,omitempty"`

	// Unmatched lists the files that could not be attributed.
	Unmatched []UnmatchedFile `json:"unmatched,omitempty"`

	// Resume is the last path whose outcome is in this tally, in walk
	// order. A redelivered task resumes after it rather than walking from
	// the top, so a redelivery neither counts a file twice nor drives the
	// counters backwards. Empty once Done.
	Resume string `json:"resume,omitempty"`
}

// Summary renders p as the sentence the LibraryScan controller puts in the
// Ready condition's message: the status counters, then the breakdown the
// CRD has no field for. Zero clauses are left out.
func (p Progress) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d files seen, %d matched", p.FilesSeen, p.FilesMatched)
	if p.FilesSkipped > 0 {
		fmt.Fprintf(&b, ", %d skipped (%s)", p.FilesSkipped, clauses(
			clause{p.Unchanged, "unchanged"},
			clause{p.Transcoded, "transcoded, left to catalogarr"},
			clause{p.TranscodeOutputs, "transcode outputs, left to catalogarr"},
			clause{p.Deferred, "changed during the scan, left to the next"},
		))
	}
	fmt.Fprintf(&b, ", %d unmatched", len(p.Unmatched))
	if p.AwaitingEpisodes > 0 {
		fmt.Fprintf(&b, "; %d awaiting the episodes of their series, which a later scan attributes", p.AwaitingEpisodes)
	}
	if p.HandedOver > 0 {
		fmt.Fprintf(&b, "; %d transcoded files changed on disk, handed to catalogarr", p.HandedOver)
	}
	if p.Unreadable > 0 {
		fmt.Fprintf(&b, "; %d could not be read", p.Unreadable)
	}
	if ignored := p.NotMedia + p.Parts + p.Extras + p.Samples; ignored > 0 {
		fmt.Fprintf(&b, "; %d other files not considered (%s)", ignored, clauses(
			clause{p.NotMedia, "not media"},
			clause{p.Samples, "samples"},
			clause{p.Extras, "extras"},
			clause{p.Parts, "partial downloads"},
		))
	}
	return b.String()
}

type clause struct {
	n    int64
	what string
}

func clauses(cs ...clause) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.what))
		}
	}
	return strings.Join(parts, ", ")
}

// Encode marshals p for a KV Put.
func (p Progress) Encode() ([]byte, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("rescan: encode progress: %w", err)
	}
	return data, nil
}

// DecodeProgress unmarshals a KV value written by [Progress.Encode].
func DecodeProgress(data []byte) (Progress, error) {
	var p Progress
	if err := json.Unmarshal(data, &p); err != nil {
		return Progress{}, fmt.Errorf("rescan: decode progress: %w", err)
	}
	return p, nil
}

// ProgressKey is the clustarr-progress key one LibraryScan's worker
// checkpoints to and its controller polls, following that bucket's existing
// "<kind>.<uid>" convention (spec §5's clustarr-progress row). The UID, not
// the name, keys it: a scan deleted and recreated under the same name is a
// different scan and must not read the old one's tally.
//
// The UID goes through events.KVKeyToken like every other KV key in the
// repo. A Kubernetes UID is alphanumeric, so this is defensive on the normal
// path -- but the worker has a fallback for an empty UID, and "scan." with
// nothing after it is a trailing dot, which nats.go rejects on Put and on
// Delete alike.
func ProgressKey(scanUID string) string { return "scan." + events.KVKeyToken(scanUID) }
