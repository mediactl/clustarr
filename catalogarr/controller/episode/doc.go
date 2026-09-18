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
// (title/overview/airDate/tvdbID/absoluteNumber/runtimeMinutes) under the
// same k8s.ManagerCatalogarr field manager this reconciler uses for its own
// Phase/Conditions/HasFile/FileRef/FileQuality/FileFormatScore/CutoffMet/
// ActiveDownloadRef write. The two writers -- this reconciler and the
// Series reconciler -- never touch each other's fields; splitting a single
// field manager by field set this way is the same pattern §5 uses for
// grabarr/grabarr-engine on Download, not a status subresource split. This
// reconciler never builds an EpisodeStatusApplyConfiguration that calls
// WithTitle/WithOverview/WithAirDate/WithTvdbID/WithAbsoluteNumber/
// WithRuntimeMinutes -- those are the Series reconciler's alone.
package episode
