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

// Package importlist is the ImportList controller, the manager's import-list
// state machine (ADR-0019 §7.6): it schedules one
// clustarr.work.import.list.<name> sync task per spec.refreshInterval tick
// (clamped up to the provider's own floor -- schedule.go), diffs each
// snapshot the import agent reports and applies the result to the catalog
// (diff.go, apply.go), refreshes Trakt tokens and drives the device-code
// handshake (auth.go, tokens.go), and is the sole writer of
// ImportList.status (amendment §A1.3, §A1.6, design spec §8.7; task G1-3).
//
// # The agent fetches; this controller decides
//
// It never fetches a list itself. The import agent's list worker
// (app/import/worker/importlist) fetches each kind, resolves ids through the
// metadata gateway and writes the listed set as a snapshot into
// clustarr-importlist (importliststate.SnapshotKey, chunked past 512 KiB,
// ruling R17). SnapshotSource watches those writes and wakes the list;
// readSnapshot refuses a partial chunk set; a complete snapshot newer than
// status.lastSyncAt is diffed once (incorporate), and its SyncedAt becomes
// status.lastSyncAt, with the counts this controller computed.
//
// The diff applies today's rules in the manager (P72-P81): a kind the list
// cannot yield is refused; entries are deduped and checked against
// ImportExclusion (failing open); with spec.automaticAdd off they are
// recorded as listed only; a list writes only the items it added itself,
// finding every item by the provider id its kind is keyed on
// (libraryByID); an id the agent could not resolve keeps a remembered item
// known; items are created or updated from the list's defaults under
// k8s.ManagerImportWorker, keeping a classified series' profile and type
// (seriesSpecFor, classified); and what fell off the list is logged,
// unmonitored, removed or removed with its files per spec.syncLevel, unless
// another enabled list still has it. Deletes carry a UID precondition
// (W28); removeAndDelete publishes a schema.RecycleFilesTask for the item's
// recorded files, which the import agent moves into the RootFolder's
// recycle bin, then deletes their MediaFiles and the item, each with a UID
// precondition (W29). The remembered items (importliststate.ItemsKey) are
// this controller's alone.
//
// # Kinds a provider cannot yield (gap-fix ruling R-10)
//
// importliststate.YieldableKinds is the table: trakt, plex, tmdb, mdblist
// and imdbCSV yield movie and series; stevenLu movie; arr its instance's
// kinds (radarr movie, sonarr series, lidarr album, readarr book or
// audiobook, clustarr any); custom any. R-10 rejects every other combination at admission,
// through three CEL rules on ImportListSpec (task X14 applied them);
// TestAdmissionMatchesYieldableKinds holds them to the table for every
// provider and kind.
//
// This controller reaches the same verdict for any list admitted before
// them: Ready=False, ReasonUnsupportedKind, naming the kinds, and nothing
// scheduled or diffed.
//
// # Trakt tokens
//
// spec.trakt requires spec.secretRef to hold the Trakt application's
// clientID and clientSecret (the app credentials registered with Trakt,
// not a user token). This controller drives
// pkg/importlist/trakt.DeviceFlow -- Start to mint a user code, Poll on a
// RequeueAfter cadence matching Trakt's own requested interval -- and
// surfaces status.auth.{userCode,verificationURL,expiresAt} so the UI can
// show the user what to enter and where. The resulting token pair (and the
// in-flight device code between Start and Poll) is persisted in an owned
// Secret by SecretTokenStore.
//
// It is the Secret's only writer and the token's only refresher (W30):
// within TokenRefreshWindow of tokenExpiresAt, and at once when a sync
// reports Trakt refusing the token (NeedsToken; Synced reads
// ReasonTokenRefused), after which it re-issues the sync. Trakt rotates
// refresh tokens, so one refresher removes the race two writers had. A
// re-issued sync refused again clears the pair and starts a device flow.
// The agent reads the Secret and never writes it.
//
// # Field managers
//
// Status and the token Secret are written under k8s.ManagerImport; the
// items a list adds, unmonitors or deletes under k8s.ManagerImportWorker
// (FieldManager), the names kept (R7) though the writes moved here from the
// agent.
//
// # Registration
//
// app/import/manager registers it with the manager's client, API reader and
// bus, and TraktBaseURL when set, which should name the same host as the
// agent's own --trakt-base-url.
package importlist
