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

// Package rssmatcher is spec §8.7's first clause: "rss-matcher maps each
// release to monitored items (ids or normalized title+year via informer
// index) and runs the same Evaluate for the single release; approved -> the
// same delay/lease/grab path (pending CAS keep-best updates a delayed grab)".
//
// It consumes the CLUSTARR_RELEASES firehose that indexarr publishes on
// clustarr.rel.>: one message per new indexer row. Most of them match nothing,
// so the matching has to be cheap -- which is what the field indexes in
// index.go are for.
//
// # The "informer-backed in-memory map" is a field index
//
// §6.1 asks for an "informer-backed in-memory map tmdb/tvdb/imdb/mbid/
// normalizedTitle+year -> monitored items". A controller-runtime field index
// IS that map: the manager's informers maintain it, lookups are in-memory, and
// it needs no second cache layer, no invalidation and no code of its own
// beyond the extractor functions. IndexFields builds thirteen of them: six for
// movies and series, seven for the non-video kinds (nonvideo.go).
//
// # Approved releases take the same path as a search's
//
// The last step is grab.Decide, the same function the search worker's sink
// calls. That is what makes §8.7's "the same delay/lease/grab path" true by
// construction rather than by two implementations agreeing: an RSS hit inside
// an open delay window lands in the same clustarr-pending entry and replaces
// the candidate if it is better, without restarting the window.
//
// # Registration
//
// Nothing registers itself. catalogarr's setupQueueWorkers registers this
// package's thirteen indexes and catalogarr/worker/search's three Download indexes
// together, from one call (registerWorkerIndexes), and then:
//
//	h := rssmatcher.NewHandler(rssmatcher.Deps{
//		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Bus: bus,
//		Catalogue: catalogue.LoadedCatalogue(), Topology: &topology,
//		SceneMaps: sceneMaps,
//	})
//	if err := h.SetupWithManager(mgr, bus); err != nil {
//		return fmt.Errorf("catalogarr: subscribe rss-matcher: %w", err)
//	}
//
// The three Download indexes are NOT registered by the search worker. They
// used to be -- search.Worker.SetupWithManager called RegisterDownloadIndexes
// itself -- which made them a side effect of whichever worker happened to be
// enabled, while this package read the blocklist and the live queue through
// them and, when they were absent, degraded to "not blocklisted, empty queue"
// with a WARNING rather than an error. Task C12a moved the registration into
// one deterministic call and added a startup assertion (assertWorkerIndexes)
// that fails the manager when any index it names is missing. The blocklist
// is now read with one labelled List per release (search.LoadBlocklist), and
// a failed read retries instead of deciding as if nothing were blocklisted;
// only the queue still reads an index.
package rssmatcher
