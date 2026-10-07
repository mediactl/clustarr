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

// Package episodeorder states which episode numbering a Series uses. The
// Series controller and TheIntroDB's markers handler share it, so the
// handler links no controller (design 2026-10-06 §4.3 C9).
package episodeorder

import catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"

// EffectiveEpisodeOrder returns the numbering scheme actually used: the
// requested order, falling back to official when order is the Go zero
// value. Series type anime no longer forces absolute order (2026-10-06,
// docs/superpowers/specs/2026-10-06-anime-dual-audio-design.md §3): with
// TVDB, absolute order puts every episode in season 1, which collapsed a
// multi-season show's seasons. An anime reads absolute numbers for search,
// import and naming from status.absoluteNumber instead, and a series that
// wants absolute order sets spec.episodeOrder: absolute. seriesType stays a
// parameter so the callers read as before. SeriesSpec.EpisodeOrder carries
// +kubebuilder:default=official, so a real object is never "" in practice;
// this function still handles it defensively since it is tested and used in
// isolation.
func EffectiveEpisodeOrder(_ catalogv1alpha1.SeriesType, order catalogv1alpha1.EpisodeOrder) catalogv1alpha1.EpisodeOrder {
	if order == "" {
		return catalogv1alpha1.EpisodeOrderOfficial
	}
	return order
}
