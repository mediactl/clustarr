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

// Package episode implements the Episode controller: phase as a pure
// function, and a thin Reconciler around it plus the file/download rollup
// from a watched MediaFile and Download.
//
// Episode objects are created and owned by the Series controller (Series
// sets spec.seriesRef/seasonNumber/episodeNumber and, at creation or per
// monitorNewItems, spec.monitored), and the Series reconciler also writes
// this Episode's provider-sourced status fields
// (title/overview/airDate/tvdbID/absoluteNumber/runtimeMinutes) -- but
// under the distinct k8s.ManagerCatalogarrSeries field manager, never
// k8s.ManagerCatalogarr, which this reconciler alone uses for
// Phase/Conditions/HasFile/FileRef/FileQuality/FileFormatScore/CutoffMet/
// ActiveDownloadRef. §5's grabarr/grabarr-engine split on Download is the
// same pattern: two field manager NAMES on disjoint fields, not one name
// shared by convention. Server-side apply replaces a manager's whole
// ownership set on every apply, so two writers sharing one manager name
// would silently release each other's fields the next time either side
// reconciles -- found empirically during this task's development; see
// k8s.ManagerCatalogarrSeries's own doc comment. This reconciler never
// builds an EpisodeStatusApplyConfiguration that calls
// WithTitle/WithOverview/WithAirDate/WithTvdbID/WithAbsoluteNumber/
// WithRuntimeMinutes -- those are the Series reconciler's alone, and being
// on a different field manager means this reconciler does not even need to
// pass them through to avoid clobbering them.
//
// Unlike MediaFile's spec-versus-status split, both reconcilers here write
// within the SAME subresource (status): every field the Series reconciler
// sets on an Episode is genuinely an EpisodeStatus field, confirmed against
// episode_types.go -- EpisodeSpec carries only
// seriesRef/seasonNumber/episodeNumber (immutable) and monitored, both set
// once at Create rather than through repeated server-side apply, so they
// carry none of the same-manager clobbering risk the status fields do.
package episode
