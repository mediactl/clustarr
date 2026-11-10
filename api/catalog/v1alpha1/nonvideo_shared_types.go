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

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// The types the non-video containers and leaves share
// (docs/superpowers/specs/2026-11-10-non-video-libraries-design.md §4.1).

// ContainerPhase is the coarse lifecycle state of a container item -- an
// Artist, Author or Comic -- the Series' three values.
//
// +kubebuilder:validation:Enum=Pending;Ready;Unmonitored
type ContainerPhase string

// Container phases.
const (
	ContainerPhasePending     ContainerPhase = "Pending"
	ContainerPhaseReady       ContainerPhase = "Ready"
	ContainerPhaseUnmonitored ContainerPhase = "Unmonitored"
)

// ContainerRollup is what a container reads from its children, the Series'
// rollup (series.Rollup) for an Artist's albums, an Author's books and a
// Comic's issues: written by the item key under catalog beside the kind's
// own named counts (albumCount, bookCount, issueFileCount), which keep
// kubectl's columns.
type ContainerRollup struct {
	// Phase is the coarse lifecycle state of the container: Pending until
	// its metadata is fetched, Unmonitored when spec.monitored is false,
	// else Ready.
	// +optional
	Phase ContainerPhase `json:"phase,omitempty"`

	// MissingChildCount counts the monitored, released children without a
	// file (the ones a wanted sweep would search for).
	// +optional
	MissingChildCount int32 `json:"missingChildCount,omitempty"`

	// DownloadingChildCount counts the children with a grab in flight or
	// pending.
	// +optional
	DownloadingChildCount int32 `json:"downloadingChildCount,omitempty"`

	// CutoffUnmetChildCount counts the children whose file is below the
	// cutoff, an album with a track missing included.
	// +optional
	CutoffUnmetChildCount int32 `json:"cutoffUnmetChildCount,omitempty"`

	// NextReleaseDate is the earliest release date still in the future
	// among the children; PreviousReleaseDate the latest one past.
	// +optional
	NextReleaseDate *metav1.Time `json:"nextReleaseDate,omitempty"`
	// +optional
	PreviousReleaseDate *metav1.Time `json:"previousReleaseDate,omitempty"`
}

// NFOStatus records the artist.nfo or album.nfo the import agent last
// wrote for the item (2026-11-10 design §8): the hash of the rendered
// document and image digests, so a changed document is written once, and
// the files the agent wrote, which are the only ones it rewrites.
type NFOStatus struct {
	// Hash is pkg/nfo's hash of the rendered document and the image
	// objects' digests.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Hash string `json:"hash,omitempty"`

	// Path is the folder the files were written into.
	// +optional
	Path string `json:"path,omitempty"`

	// Files are the names the agent wrote in Path (artist.nfo, artist.jpg,
	// album.nfo, folder.jpg); an operator's own file of another name is
	// never touched.
	// +optional
	// +kubebuilder:validation:MaxItems=8
	Files []string `json:"files,omitempty"`

	// WrittenAt is when the agent answered the write.
	// +optional
	WrittenAt *metav1.Time `json:"writtenAt,omitempty"`
}

// Link is one external page of an item: a MusicBrainz url-rel, an Open
// Library link, a provider's own page.
type Link struct {
	// Type names the site: wikidata, wikipedia, discogs, homepage,
	// bandcamp, lastfm, spotify, apple, youtube, goodreads, hardcover,
	// audible, comicvine, metron, mangadex, anilist, mal.
	// +required
	// +kubebuilder:validation:MaxLength=32
	Type string `json:"type"`

	// URL is the page.
	// +required
	// +kubebuilder:validation:MaxLength=512
	URL string `json:"url"`
}

// Credit is one person credited on an issue (ComicVine's person_credits,
// Metron's credits): writer, artist, cover artist, letterer, editor.
type Credit struct {
	// Name is the person's name.
	// +required
	// +kubebuilder:validation:MaxLength=256
	Name string `json:"name"`

	// Role is the credited role as the provider names it.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Role string `json:"role,omitempty"`
}

// ComicRunStatus is whether a volume is still being published.
//
// +kubebuilder:validation:Enum=continuing;ended
type ComicRunStatus string

// Comic run statuses.
const (
	ComicRunStatusContinuing ComicRunStatus = "continuing"
	ComicRunStatusEnded      ComicRunStatus = "ended"
)

// ComicMonitorMode selects which issues of a newly added comic start out
// monitored (Mylar's and Sonarr's add modes).
//
// +kubebuilder:validation:Enum=all;future;missing;existing;none
type ComicMonitorMode string

// Comic monitor modes.
const (
	ComicMonitorAll      ComicMonitorMode = "all"
	ComicMonitorFuture   ComicMonitorMode = "future"
	ComicMonitorMissing  ComicMonitorMode = "missing"
	ComicMonitorExisting ComicMonitorMode = "existing"
	ComicMonitorNone     ComicMonitorMode = "none"
)

// ComicAddOptions are applied exactly once, when the comic is first
// reconciled, recorded in status.addOptionsApplied.
type ComicAddOptions struct {
	// Monitor selects which issues start out monitored.
	// +optional
	// +kubebuilder:default=all
	Monitor ComicMonitorMode `json:"monitor,omitempty"`

	// SearchForMissing searches for the missing issues on add.
	// +optional
	// +kubebuilder:default=true
	SearchForMissing *bool `json:"searchForMissing,omitempty"`
}

// AudiobookAddOptions are applied exactly once, when the audiobook is
// first reconciled, recorded in status.addOptionsApplied.
type AudiobookAddOptions struct {
	// SearchForMissing searches for the audiobook on add.
	// +optional
	// +kubebuilder:default=true
	SearchForMissing *bool `json:"searchForMissing,omitempty"`
}
