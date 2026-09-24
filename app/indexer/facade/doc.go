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

// Package facade serves indexarr's own indexers back out as Torznab, so
// external tools (Sonarr/Radarr-alikes, Jackett-compatible scripts, a human
// with curl) can search Clustarr's aggregated indexers the same way they
// would search Prowlarr or Jackett. It is design §6.2's "Facade":
// `/{indexer}/api`, `/{indexer}/download` and the aggregate `/search/api`
// ("Jackett filter grammar deferred").
//
// # Routes
//
//   - "GET /{indexer}/api?t=caps"                    -> that Indexer's
//     status.caps, rendered as a t=caps document.
//   - "GET /{indexer}/api?t=search|tvsearch|movie|music|audio|book&q=..."
//     -> a LIVE federated search scoped to that one indexer, via Config.Search
//     (clustarr.rpc.indexarr.search's own body, called in-process -- see
//     below). q becomes SearchRequest.Text, the field
//     catalogarr/worker/search's BuildSearchRequest also fills, from the
//     item's resolved title (G1-6); indexarr/search's buildQuery uses it
//     only for an indexer that supports none of the request's id
//     parameters.
//   - "GET /{indexer}/download?guid=...&url=..."      -> Config.Download
//     (clustarr.rpc.indexarr.download's own body), resolving the payload
//     with the indexer's own session.
//   - "GET /search/api?t=...&q=...&cat=..."           -> an aggregate read
//     against indexarr's already-merged local SQLite release index, via
//     Config.Query (clustarr.rpc.indexarr.query's own body) -- not a live
//     fan-out across every indexer on every request. §6.2's design table
//     names this pairing explicitly: "rpc.indexarr.query ... Torznab facade
//     -> indexarr SQLite index." Only `q` and `cat` are honoured; the
//     Jackett filter grammar (tracker/protocol selection encoded into the
//     query string) is the deferred half named in §6.2 and is not
//     implemented here.
//
// A real Indexer literally named "search" would be shadowed by the
// /search/api route -- net/http's ServeMux prefers the more specific
// literal pattern over the "/{indexer}/api" wildcard for that one path.
// This mirrors Prowlarr/Jackett's own reserved-path collision and is not
// worked around here.
//
// # Same process, plain Go calls, no NATS round trip
//
// indexarr is pinned to exactly one replica (§3: Recreate, RWO PVC
// clustarr-index) and the facade is one of ITS OWN features, in the same
// process as the search, query and download RPC responders -- not a
// separate service reaching them over the bus the way catalogarr does. So
// Config.Search/Query/Download are plain Go function values with the same
// signature as indexarr/search.Service.Search, indexarr/query.Service.Handle
// and indexarr/download.Service.Handle's methods (a *search.Service's
// Search method value is directly assignable to facade.SearchFunc, with no
// wrapper needed) -- this package intentionally does not import
// indexarr/search, indexarr/query, indexarr/controller or indexarr/run.go so
// it has nothing to conflict with while those packages are under concurrent
// development; it only shares the wire schema
// (github.com/mediactl/clustarr/pkg/events/schema) and the Indexer CRD type.
// Wiring New's three funcs to the real services is indexarr/run.go's job,
// not this package's: setupFacade (plan task G1-5) hands it the same
// search, query and download services the RPC responder serves.
//
// # Auth
//
// The design spec and its amendment are silent on facade authentication.
// Given that, this package refuses to start unauthenticated: New returns an
// error unless Config.APIKeys has at least one non-blank key, and every
// route checks it (the `apikey` query parameter, Torznab's own convention,
// or an `X-Api-Key` header) before doing anything else. A facade with no
// key would serve a private tracker's search results and download links --
// passkeys included, since DownloadResponse.Bytes/RedirectURL carry
// whatever the indexer's own session produces -- to anything that can reach
// the Service's port. The caller sources the key(s) from a Kubernetes
// Secret -- indexarr/run.go's setupFacade reads every non-blank entry of
// --facade-api-key-secret, generating it with one random key when absent
// (indexarr/facadekey.go); this package takes plain strings so it does not
// need a client.Client just to read one.
//
// # Disabling
//
// New(addr, cfg) returns (nil, nil) when addr equals
// pkg/k8s.DisabledBindAddress ("0"), the same sentinel
// indexarr.Options.FacadeBindAddress's own doc comment already promises
// ("'0' disables it") and the one controller-runtime's own metrics/health
// servers use. The caller checks for a nil *Server rather than calling
// Run on a server it never meant to start.
//
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=get;list
package facade
