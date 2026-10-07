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

// Package clients builds the wire clients index talks to an indexer
// through -- Torznab/Newznab or a Cardigann engine, behind the Indexer's
// proxy and its host's rate limit -- and keeps Cardigann login sessions
// (the clustarr-indexer-sessions KV bucket, with the owned Secret read as
// the fallback). The manager's Indexer reconciler probes and logs in with
// it, and the index agent's client cache serves searches with it; it links
// neither side's runtime (spec §4.3 X2, §5.12). It writes no Kubernetes
// object: the session Secret's one writer is the Indexer controller's
// mirror (ADR-0019 §7.5), so the agent, which links this package, links no
// Secret writer.
//
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=get;list;watch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexerdefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexerproxies,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
package clients
