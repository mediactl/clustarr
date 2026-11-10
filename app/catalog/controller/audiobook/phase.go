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

package audiobook

import (
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// PhaseInputs is the Audiobook's view for rollup.LeafPhase, the one phase
// and cutoffReason the four non-video leaves share (2026-11-10 design D4;
// until then AudiobookPhase had neither Pending nor Unavailable). The facts
// the reconcile computed are passed in; the ones the Audiobook carries are
// read here:
//
//   - monitored is the Audiobook's own flag (itemread.Monitored; its
//     Author's too once design W1.3 lands there).
//   - metadataReady is whether status.metadata is fresh (MetadataStale): an
//     Audiobook without a document, or past its RefreshTTL, reads Pending
//     until the gateway answers, Movie's rule.
//   - Released is whether the publication date has passed, an unknown date
//     counting as out (itemread.Available); an unreleased Audiobook with
//     nothing on disk or in flight reads Unavailable.
//   - CutoffEvaluated is whether the QualityProfile resolved; with files
//     and no profile the Audiobook reads CutoffUnevaluated, never
//     CutoffUnmet, which would put it in the upgrade rotation on a verdict
//     that was never reached.
//   - PendingGrab is status.pendingGrab: a release chosen and waiting out a
//     DelayProfile delay, read-only here (the grab path writes it).
//
// A live grab entry overlays Downloading over the result (rollup.LeafOverlay),
// the caller's step.
func PhaseInputs(m *catalogv1alpha1.Audiobook, monitored, metadataReady, hasFile, cutoffMet, profileResolved bool, now time.Time) rollup.LeafInputs {
	return rollup.LeafInputs{
		Monitored:       monitored,
		MetadataReady:   metadataReady,
		Released:        itemread.Available(m, now),
		HasFile:         hasFile,
		CutoffMet:       cutoffMet,
		CutoffEvaluated: profileResolved,
		PendingGrab:     m.Status.PendingGrab != nil,
	}
}

// MetadataStale is the Audiobook's metadata staleness as the reconcile
// decides it: stale when the document is missing or past its RefreshTTL
// (the Audiobook then reads Pending and a refresh is asked for), outdated
// when a recent document was written before the gateway learned a field
// (metadata.SchemaVersion; refreshed once, at once, the Audiobook staying
// ready meanwhile -- design D8, Movie's rule). MediaKindAudiobook is a flat
// 30-day cadence regardless of state (pkg/metadata/refresh.go), so
// RefreshStateActive is passed only to avoid metadata.RefreshTTL's two
// magic-string special cases ahead of its per-kind switch -- exactly the
// reasoning app/catalog/metadata/worker.go's own Audiobook branch gives.
func MetadataStale(m *catalogv1alpha1.Audiobook, now time.Time) (stale, outdated bool) {
	md := m.Status.Metadata
	if md == nil {
		return true, false
	}
	ttl := metadata.RefreshTTL(commonv1.MediaKindAudiobook, metadata.RefreshStateActive, md.RefreshedAt.Time)
	stale = now.Sub(md.RefreshedAt.Time) >= ttl
	return stale, !stale && md.SchemaVersion < metadata.SchemaVersion
}

// IdleInputs is the Audiobook's view for rollup.LeafIdleAtStart, the
// Audiobook watch's SkipInitial (design D14): the object as the cache lists
// it at a manager's start, with monitored read as the pass would read it,
// and a refresh due whenever the pass would ask the gateway for one
// (MetadataStale).
func IdleInputs(m *catalogv1alpha1.Audiobook, monitored bool, now time.Time) rollup.LeafIdle {
	stale, outdated := MetadataStale(m, now)
	return rollup.LeafIdle{
		Generation:         m.Generation,
		ObservedGeneration: m.Status.ObservedGeneration,
		Monitored:          monitored,
		Phase:              m.Status.Phase,
		RefreshDue:         stale || outdated,
		ActiveDownloadRef:  m.Status.ActiveDownloadRef,
		DownloadPhase:      m.Status.DownloadPhase,
		PendingGrab:        m.Status.PendingGrab != nil,
		SearchInFlight:     m.Status.SearchDispatch.InFlight(),
		Conditions:         m.Status.Conditions,
	}
}
