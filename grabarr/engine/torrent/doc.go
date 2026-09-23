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

// Package torrent is the torrent engine: the reconciler that runs inside one
// ordinal of the StatefulSet grabarr/controller/downloadclient (D2-3) owns,
// wrapping pkg/download/torrent (D2-1) around one embedded anacrolix client.
// Design spec §6.3 "Torrent engine pod"; plan task D2-5.
//
// # Re-attach gates readiness (R4)
//
// grabarr/run.go:216 already says why: an engine that reports ready before it
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
// # Field manager: telemetry only
//
// This package applies Download.status exclusively under
// k8s.ManagerGrabarrEngine, and exclusively through
// download.ApplyStatus(item) -- a fresh, complete declaration computed from a
// live download.Client.Get, never seeded from a stale Download.Status read.
// That is a stronger guarantee than "remember to re-Get before applying": it
// removes the seed entirely, so a slow resolve (an HTTP fetch for
// spec.source.torrentURL, an RPC round trip for spec.source.indexerDownload)
// occurring between a Get and a Patch cannot roll anything back, because
// nothing about the apply's CONTENT came from that Get in the first place.
// [Reconciler] still re-Gets the Download immediately before every
// grabarr/status.Patch call, both because the target's identity should be
// current and because the discipline is the project's blanket rule
// (CLAUDE.md, "a lost update is not an SSA release") -- see
// telemetry_traps_test.go's lost-update case for the empirical proof, and its
// file-list case for why WithFiles is never called on a seeded
// configuration: download.ApplyStatus always starts from a FRESH apply
// configuration (downloadac.DownloadStatus()), so the append it performs on
// Files is the one documented safe case.
//
// # No finalizer here -- matched against both landed siblings
//
// This package never touches metadata.finalizers. D2-4
// (grabarr/controller/download/controller.go's reconcileDelete) holds the
// sole finalizer, k8s.FinalizerFor(dl, scheme) =
// "download.clustarr.io/download", and does its own data removal directly
// against the shared DataDir via fsops.SafeRemove(dl.Status.OutputPath) --
// it does not wait for any engine to react first. grabarr/engine/usenet's
// Reconciler (D2-6) reaches the identical design independently: an engine
// calls [download.Client.Remove] for its own in-process cleanup once it
// observes a deletionTimestamp, idempotently
// ([download.ErrNotFound] counts as done, matching Remove's own contract),
// but claims no finalizer of its own. [Reconciler.reconcileDeleting] follows
// that same shape. An earlier draft of this package added its own
// FinalizerEngine before D2-4 or D2-6 were readable in this shared
// worktree, reasoning that two independent finalizers cannot race each
// other; once both landed and could actually be read, this package matched
// them instead of leaving a third, different answer to the identical
// question standing in one phase.
//
// That design leaves exactly the gap plan task D2-8b names: reconcileDelete
// drops the finalizer without waiting for any engine, so a Download deleted
// while this engine's watch has not yet delivered the deletion -- down,
// mid-re-attach, or a missed event -- never reaches reconcileDeleting at
// all, and its transfer keeps running with no CR left to say so. [Reaper]
// (reaper.go) is the fix: a level-driven pass, independent of any watch
// event, that lists [Engine.Client]'s own transfers against this replica's
// Downloads and removes whatever has no match after a grace period. See its
// doc comment for the two conservatism guards and why deleteData is always
// false there.
//
// # What this package does not attempt
//
// It does not implement DownloadClient-driven rate limiting
// (spec.torrent.downloadLimitBps/uploadLimitBps): pkg/download/torrent's
// Config exposes no rate-limiter fields to plumb them through, and adding
// them there is D2-1's package, out of this task's directory. It resolves
// spec.source.indexerDownload through the clustarr.rpc.indexarr.download verb
// (indexarr/download, already served) via [IndexerResolver], but the HTTP
// fetch for torrentURL/redirectURL carries no injected rate limiter --
// low-volume, at most one fetch per new Download, unlike indexarr's own
// crawl paths that the project's rate-limiting convention was written for.
//
// # RBAC
//
// Markers are package-level so controller-gen collects them; see
// grabarr/controller/downloadclient/doc.go for why a marker is restated here
// even though grabarr/status/doc.go already grants the /status half.
//
// config/rbac/role.yaml and charts/clustarr/templates/rbac.yaml are NOT
// regenerated by this task, following grabarr/controller/downloadclient's own
// documented precedent for this exact phase -- a precedent D2-4 and D2-6 both
// independently kept once they landed in this shared worktree (neither
// touched config/rbac or charts/ either, verified against their commits).
// Makefile's RBAC_DIRS already lists grabarr, so `make manifests` will pick
// every task's markers up in one pass; D2-8 ("collect those") is where that
// pass happens and the chart is synced, once, after every D2-1..D2-7 task has
// landed its own markers.
//
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloadclients,verbs=get;list;watch
package torrent
