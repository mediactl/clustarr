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

// Package imageset is the item keys' image-set stage
// (docs/superpowers/specs/2026-10-22-mediux-image-sets-automation-design.md):
// for a Movie or Series with a TMDB id (a series' TheTVDB id does, the
// agent maps it), while a mediux MetadataProvider is Ready and the item
// has not opted out (spec.imageSet: none), it asks the metadata agent's
// image-set worker -- a clustarr-image-sets request and a catalog-imageset
// task, admitted through the dispatch ledger -- and incorporates the
// answer into status.imageSet as catalog-imageset's one field. The owner's
// spec.artwork and spec.seasonArtwork are never written: a pasted set wins
// over the chosen one wherever both are read (the Plex provider, the
// artwork fetch).
//
// The query reads MetadataProviders through the manager's cache. The blank
// line keeps the marker package-level.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=metadataproviders,verbs=get;list;watch
package imageset
