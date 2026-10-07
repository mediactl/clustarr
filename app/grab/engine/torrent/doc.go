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

// Package torrent is grabarr's torrent engine (ADR-0019 §6.7): the
// engine.Transfers over its embedded client -- the anacrolix client and
// its re-attach descriptors -- which the shared
// command handler (app/grab/engine) drives with the manager's seq-fenced
// desired-state commands. It holds each transfer's journal (the claim,
// the last applied seq, the import) beside its own state, reads no
// catalog object and writes no Kubernetes object: the Download
// reconciler, the orphan reaper and the engine finalizer are gone, their
// decisions the manager's (app/grab/lifecycle).
//
// # Re-attach gates readiness (R4)
//
// app/grab/run.go:216 already says why: an engine that reports ready before it
// has reloaded its own previously-running transfers gets handed work by the
// Download controller (D2-4, which waits for DownloadClient's EngineReady
// condition -- itself driven by this pod's Kubernetes readiness) while a
// transfer it is already running looks, to that controller, like nothing is
// happening yet. [Engine.ReAttach] must run to completion, synchronously,
// before anything else: before [Engine.HealthzCheck] reports healthy, and
// before [Reconciler.Reconcile] does anything but requeue. Both gates are
// enforced independently -- see [Engine.Ready] -- so a caller that wires only
// one of them still gets the protection.
//
// # Persistence, and why it is not literally "<infohash>.torrent"
//
// Spec §6.3 describes re-attach as reading persisted metainfo from
// "/data/torrents/.state/<infohash>.torrent". [Client.Add] (pkg/download/torrent)
// takes more than metainfo bytes -- Category, Priority, Paused and
// SeedCriteria all shape the call and none of them round-trips through a bare
// .torrent file -- so this package writes two files per transfer under that
// same .state directory: "<id>.torrent" holds the raw metainfo bytes when the
// source was a direct payload (exactly the spec's own path, for the common
// case), and "<id>.json" is a sidecar carrying everything else Add needs
// ([descriptor]). A magnet-sourced transfer has no metainfo file at all --
// the magnet URI lives in the sidecar instead -- because a magnet's info
// dict may not have arrived by the time a crash truncates it.
//
// The sidecar also carries spec §6.3's "persisted cumulative counters": the
// upload, the seed time and whether the seed goal was met ([seedRecord],
// refreshed at most once a minute, a met goal at once), handed back on
// re-attach as download.AddRequest.SeedHistory so a restarted engine counts
// on from them and keeps a met goal met.
//
// # What this package does not attempt
//
// It does not implement DownloadClient-driven rate limiting
// (spec.torrent.downloadLimitBps/uploadLimitBps): pkg/download/torrent's
// Config exposes no rate-limiter fields to plumb them through, and adding
// them there is D2-1's package, out of this task's directory. It resolves
// spec.source.indexerDownload through the clustarr.rpc.indexarr.download verb
// (app/indexer/download, already served) via [IndexerResolver], but the HTTP
// fetch for torrentURL/redirectURL carries no injected rate limiter --
// low-volume, at most one fetch per new Download, unlike indexarr's own
// crawl paths that the project's rate-limiting convention was written for.
//
// # RBAC
//
// The engine reads its DownloadClient and its Secrets (the proxy's) by
// name at start; it writes nothing (§9.1).
//
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloadclients,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
package torrent
