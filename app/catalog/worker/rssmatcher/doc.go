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

// Package rssmatcher is spec §8.7's first clause, as ADR-0019 (§6.6 step 1,
// A4.4) amends it: the RSS matcher maps each release to the monitored items
// it could satisfy (ids or normalized title+year via informer indexes),
// runs pkg/decision's release rules (EvaluateRelease) for the single
// release, and sends each approved match to the item's grab owner as a
// Candidate on the CLUSTARR_INTAKE stream. It decides nothing else and
// writes nothing: the owner's grab planner (app/catalog/grabplan) re-checks
// the item state, applies the delay profile, narrows a pack to the episodes
// that want it and grabs -- the same path a search answer takes, which is
// what keeps §8.7's "the same delay/grab path" true.
//
// It consumes the CLUSTARR_RELEASES firehose that index publishes on
// clustarr.rel.>: one message per new indexer row. Most of them match
// nothing, so the matching has to be cheap -- which is what the field
// indexes in index.go are for.
//
// # The "informer-backed in-memory map" is a field index
//
// §6.1 asks for an "informer-backed in-memory map tmdb/tvdb/imdb/mbid/
// normalizedTitle+year -> monitored items". A controller-runtime field index
// IS that map: the agent's informers maintain it, lookups are in-memory, and
// it needs no second cache layer. FieldIndexes declares thirteen of them:
// six for movies and series, seven for the non-video kinds (nonvideo.go).
// The events domain declares them and the agent registers them once (spec
// §3.5.2 step 9).
//
// The blocklist is the firehose release's own block state (ADR-0019
// §6.14): index puts every scope that blocks a release on it, and the
// candidate carries the block naming the owner or every item.
package rssmatcher
