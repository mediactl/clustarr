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

package book

import (
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// PhaseInputs is the Book's view for rollup.LeafPhase, the one phase and
// cutoffReason the four non-video leaves share (2026-11-10 design D4). The
// facts the reconcile computed are passed in; the ones the Book carries are
// read here:
//
//   - monitored is the Book's own flag and, once W1.3 of the design lands
//     in itemread.Monitored, its Author's (design D3).
//   - metadataReady is whether status.metadata is fresh (MetadataStale): a
//     Book without a document, or past its RefreshTTL, reads Pending until
//     the gateway answers, Movie's rule.
//   - Released is whether the work's first publication date has passed, an
//     unknown date counting as out (itemread.Available; Readarr decides
//     undated ones); an unreleased Book with nothing on disk or in flight
//     reads Unavailable.
//   - CutoffEvaluated is whether the QualityProfile resolved; with a file
//     and no profile the Book reads CutoffUnevaluated, never CutoffUnmet,
//     which would put it in the upgrade rotation on a verdict that was
//     never reached.
//   - PendingGrab is status.pendingGrab: a release chosen and waiting out a
//     DelayProfile delay, read-only here (the grab path writes it).
//
// A live grab entry overlays Downloading over the result (rollup.LeafOverlay),
// the caller's step.
func PhaseInputs(bk *catalogv1alpha1.Book, monitored, metadataReady, hasFile, cutoffMet, profileResolved bool, now time.Time) rollup.LeafInputs {
	return rollup.LeafInputs{
		Monitored:       monitored,
		MetadataReady:   metadataReady,
		Released:        itemread.Available(bk, now),
		HasFile:         hasFile,
		CutoffMet:       cutoffMet,
		CutoffEvaluated: profileResolved,
		PendingGrab:     bk.Status.PendingGrab != nil,
	}
}

// MetadataStale is the Book's metadata staleness as the reconcile decides
// it: stale when the document is missing or past its RefreshTTL (the Book
// then reads Pending and a refresh is asked for), outdated when a recent
// document was written before the gateway learned a field
// (metadata.SchemaVersion; refreshed once, at once, the Book staying ready
// meanwhile -- design D8, Movie's rule).
func MetadataStale(bk *catalogv1alpha1.Book, now time.Time) (stale, outdated bool) {
	md := bk.Status.Metadata
	if md == nil {
		return true, false
	}
	ttl := metadata.RefreshTTL(commonv1.MediaKindBook, "", md.RefreshedAt.Time)
	stale = now.Sub(md.RefreshedAt.Time) >= ttl
	return stale, !stale && md.SchemaVersion < metadata.SchemaVersion
}

// IdleInputs is the Book's view for rollup.LeafIdleAtStart, the Book watch's
// SkipInitial (design D14): the object as the cache lists it at a manager's
// start, with monitored read as the pass would read it, and a refresh due
// whenever the pass would ask the gateway for one (MetadataStale).
func IdleInputs(bk *catalogv1alpha1.Book, monitored bool, now time.Time) rollup.LeafIdle {
	stale, outdated := MetadataStale(bk, now)
	return rollup.LeafIdle{
		Generation:         bk.Generation,
		ObservedGeneration: bk.Status.ObservedGeneration,
		Monitored:          monitored,
		Phase:              bk.Status.Phase,
		RefreshDue:         stale || outdated,
		ActiveDownloadRef:  bk.Status.ActiveDownloadRef,
		DownloadPhase:      bk.Status.DownloadPhase,
		PendingGrab:        bk.Status.PendingGrab != nil,
		SearchInFlight:     bk.Status.SearchDispatch.InFlight(),
		Conditions:         bk.Status.Conditions,
	}
}
