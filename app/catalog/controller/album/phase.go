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

package album

import (
	"fmt"
	"time"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// PhaseInputs is the Album's view for rollup.LeafPhase, the one phase and
// cutoffReason the four non-video leaves share (2026-11-10 design D4). The
// facts the reconcile computed are passed in; the ones the Album carries are
// read here:
//
//   - monitored is the Album's own flag and, once W1.3 of the design lands
//     in itemread.Monitored, its Artist's (design D3).
//   - metadataReady is whether status.metadata is fresh (MetadataStale): an
//     Album without a document, or past its RefreshTTL, reads Pending until
//     the gateway answers, Movie's rule.
//   - Released is whether the release group's earliest release date has
//     passed, an unknown date counting as out (itemread.Available; Lidarr
//     decides undated ones); an unreleased Album with nothing on disk or in
//     flight reads Unavailable.
//   - hasFile is whether any file backs the album (FileState), cutoffMet
//     whether every file meets the cutoff (Lidarr's CutoffSpecification).
//   - CutoffEvaluated is whether the QualityProfile resolved; with files
//     and no profile the Album reads CutoffUnevaluated, never CutoffUnmet,
//     which would put it in the upgrade rotation on a verdict that was
//     never reached.
//   - tracksMissing is TracksMissing over the listing this pass declares:
//     Lidarr's TracksWithoutFiles clause (design D6), which holds the album
//     below its cutoff whatever its files' quality, so it is searched again.
//   - PendingGrab is status.pendingGrab: a release chosen and waiting out a
//     DelayProfile delay, read-only here (the grab path writes it).
//
// A live grab entry overlays Downloading over the result (rollup.LeafOverlay),
// the caller's step.
func PhaseInputs(alb *catalogv1alpha1.Album, monitored, metadataReady, hasFile, cutoffMet, profileResolved, tracksMissing bool, now time.Time) rollup.LeafInputs {
	return rollup.LeafInputs{
		Monitored:       monitored,
		MetadataReady:   metadataReady,
		Released:        itemread.Available(alb, now),
		HasFile:         hasFile,
		CutoffMet:       cutoffMet,
		CutoffEvaluated: profileResolved,
		TracksMissing:   tracksMissing,
		PendingGrab:     alb.Status.PendingGrab != nil,
	}
}

// TracksMissing reports a track of the listing without a file (design D6,
// Lidarr's TracksWithoutFiles): the selected release lists tracks and at
// least one has no fileRef. An Album whose release is not yet selected
// lists no tracks and misses none, since nothing says what it should hold.
func TracksMissing(tracks []*catalogac.TrackApplyConfiguration) bool {
	return len(tracks) > 0 && countTracksWithFile(tracks) < int32(len(tracks)) //nolint:gosec // bounded by maxTracks
}

// TrackSummary is status.trackSummary, the Tracks print column:
// "<trackFileCount>/<tracks>" over the listing this pass declares, "" while
// no release is selected.
func TrackSummary(tracks []*catalogac.TrackApplyConfiguration) string {
	if len(tracks) == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", countTracksWithFile(tracks), len(tracks))
}

// MetadataStale is the Album's metadata staleness as the reconcile decides
// it: stale when the document is missing or past its RefreshTTL (the Album
// then reads Pending and a refresh is asked for), outdated when a recent
// document was written before the gateway learned a field
// (metadata.SchemaVersion; refreshed once, at once, the Album staying ready
// meanwhile -- design D8, Movie's rule).
func MetadataStale(alb *catalogv1alpha1.Album, now time.Time) (stale, outdated bool) {
	md := alb.Status.Metadata
	if md == nil {
		return true, false
	}
	ttl := pkgmetadata.RefreshTTL(commonv1.MediaKindAlbum, pkgmetadata.RefreshStateActive, md.RefreshedAt.Time)
	stale = now.Sub(md.RefreshedAt.Time) >= ttl
	return stale, !stale && md.SchemaVersion < pkgmetadata.SchemaVersion
}

// IdleInputs is the Album's view for rollup.LeafIdleAtStart, the Album
// watch's SkipInitial (design D14): the object as the cache lists it at a
// manager's start, with monitored read as the pass would read it, and a
// refresh due whenever the pass would ask the gateway for one
// (MetadataStale).
func IdleInputs(alb *catalogv1alpha1.Album, monitored bool, now time.Time) rollup.LeafIdle {
	stale, outdated := MetadataStale(alb, now)
	return rollup.LeafIdle{
		Generation:         alb.Generation,
		ObservedGeneration: alb.Status.ObservedGeneration,
		Monitored:          monitored,
		Phase:              alb.Status.Phase,
		RefreshDue:         stale || outdated,
		ActiveDownloadRef:  alb.Status.ActiveDownloadRef,
		DownloadPhase:      alb.Status.DownloadPhase,
		PendingGrab:        alb.Status.PendingGrab != nil,
		SearchInFlight:     alb.Status.SearchDispatch.InFlight(),
		Conditions:         alb.Status.Conditions,
	}
}
