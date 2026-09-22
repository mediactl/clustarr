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

// Package rss polls each Indexer's feed and publishes what it finds to the
// release firehose, clustarr.rel.<protocol>.<indexerName>.<newznabTop>.
//
// The consumer already ships. catalogarr/worker/rssmatcher has been
// subscribed to clustarr.rel.> since Phase C and has received nothing,
// because nothing published. Its handler pins two requirements:
//
//  1. The subject is events.ReleaseSubject(protocol, indexerName,
//     newznabTop), where newznabTop is the 1000-aligned parent category
//     (newznab.CategoryID.Parent()).
//
//  2. Envelope.Key MUST be "<namespace>/<indexerName>", literally, with a
//     slash. handler.go, verbatim:
//
//     ns, _, ok := strings.Cut(env.Key, "/")
//     if !ok || ns == "" {
//     return events.Discard("rssmatcher: envelope key is not <namespace>/<indexerName>", ...)
//     }
//
//     events.Discard goes straight to the DLQ and bypasses MaxDeliver. A key
//     with no slash is not retried, it is lost, silently, for every release
//     from that indexer. events.MediaKey's own doc says the same thing from
//     the other side: a media key is NOT an Envelope.Key, it has no slash
//     left to cut on, so publishing one as the envelope key dead-letters the
//     task on first delivery. Build both, and keep them in separate
//     variables. In Phase C exactly that conflation dead-lettered every
//     metadata refresh in the system, and nothing alerted.
//
// indexerName is the Indexer object's metadata.name. The matcher resolves
// indexer priority by Info.IndexerRef (resolve.go), which is documented as
// "the name of the Indexer object", so the envelope key's second segment,
// the subject's second token and Info.IndexerRef are all the same string.
//
// # Status
//
// Indexer.status is split by field manager, and this package writes only as
// k8s.ManagerIndexarrWorker, through indexarr/status.Patch. That package
// holds the single declaration of what the manager owns (WorkerFields);
// this one never builds an apply configuration of its own, because two
// callers hand-rolling one manager's apply is precisely how each releases
// the other's fields.
//
// # Registration
//
// The wiring task (D1-8) constructs a Worker with a client, the bus, the
// release index and a SearcherFor that hands back the per-indexer
// *torznab.Client the Indexer reconciler built -- already carrying that
// host's single injected rate limiter -- and calls SetupWithManager. This
// package never constructs a limiter and never lists Indexers: one message
// is one indexer, which is what keeps a broken indexer from starving the
// rest.
package rss
