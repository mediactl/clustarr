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

// Package series implements the Series controller: episode numbering and
// naming, the add-time and steady-state monitoring decision, the season
// rollup and phase as pure functions, and a thin Reconciler that fans a
// Series out into owned Episode objects per spec §8.1.
//
// Series is the sole writer of status.phase, status.path, status.seasons,
// status.episodeCount, status.episodeFileCount, status.nextAiring and
// status.previousAiring (all rolled up from the post-fan-out Episodes, the
// airings with Sonarr's monitored-only statistics semantics); status.metadata belongs
// to the metadata gateway (Task C5, field manager
// k8s.ManagerCatalogarrMetadata). The per-Episode provider fields
// (title/overview/airDate/tvdbID/absoluteNumber/runtimeMinutes) are written
// by this reconciler too, under the distinct k8s.ManagerCatalogarrSeries
// field manager -- not k8s.ManagerCatalogarr, which the Episode controller
// uses for that Episode's own Phase/Conditions/HasFile/etc. Two writers on
// one object cannot safely share one field manager name: server-side apply
// replaces a manager's whole ownership set on every apply, so an apply that
// omits a field the SAME manager previously sent releases it, silently
// erasing the other writer's fields the next time either side reconciles.
// This was found empirically during this task's development (see
// k8s.ManagerCatalogarrSeries's own doc comment and CLAUDE.md) and is why
// the split is a distinct manager, not a same-manager convention.
//
// This is a status-versus-status split, not a spec-versus-status one like
// MediaFile's: every field this reconciler writes on an Episode
// (title/overview/airDate/tvdbID/runtimeMinutes/absoluteNumber/finaleType)
// is an EpisodeStatus field. EpisodeSpec's only fields
// (seriesRef/seasonNumber/episodeNumber, immutable; monitored) are set once
// at Create, not through repeated server-side apply, so they carry none of
// the same-manager clobbering risk the status fields do. Distinct field
// manager NAMES on disjoint fields within one subresource is exactly the
// §5 app/grab/grabarr-engine pattern on DownloadStatus.
//
// The reconciler also folds the DLQ projector's annotation into a
// DeadLettered condition, emits Events on phase, fan-out and failure edges,
// and publishes the Series' added/updated/deleted domain events (report.go).
package series
