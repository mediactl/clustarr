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

// Package throttle is captionarr's shared, KV-backed provider throttle:
// spec §6.5's "shared KV token bucket (clustarr-provider-throttle holds JWT +
// remaining/reset) so N workers never exceed 5 req/s", plus the throttle
// window (pkg/subtitles.ThrottleFor's duration table) and Bazarr's
// five-errors-in-120s escalation (research note §10).
//
// # Ruling R2 — this package is the only place throttle state lives
//
// Every fetch worker replica calls [Acquire] before a provider request and
// [RecordError]/[RecordSuccess]/[SetQuota]/[SetAuth] after one; none of them
// touch SubtitleProvider.status. The SubtitleProvider controller (F-3) is the
// sole writer of that status, and projects it from [Get] on its own
// reconcile cadence. A worker writing status directly would make every
// worker replica a writer of one object, which is exactly the shape
// CLAUDE.md's "two components shared one field manager" gotcha describes.
//
// # KV layout
//
// The bucket is clustarr-provider-throttle (events.BucketProviderThrottle),
// 24h TTL, one key per SubtitleProvider UID plus one derived sub-key:
//
//	<provider-uid>          -> State, JSON  (ProviderKey)
//	<provider-uid>.bucket   -> token bucket, JSON  (TokenBucketKey)
//
// Both halves go through [events.KVKeyToken], so a UID containing any
// character outside [0-9A-Za-z-] still produces a key a real NATS server
// accepts (proven in kvkey_contract_test.go, per CLAUDE.md's rule that a new
// key shape gets a contract test against a real embedded server, not just
// the in-memory bus). KVKeyToken's output alphabet never contains ".", so
// the literal ".bucket" suffix can never collide with another provider's
// bare State key -- one segment has a dot, the other structurally cannot.
//
// The spec's own KV table (§5) documents the bare "<provider-uid>" key
// holding only {until, reason, quota}; this package extends that shape with
// a JWT field and a separate rate-limit sub-key, the same way
// clustarr-indexer-limits' documented "<indexer-uid>" grew a ".query" and
// ".grab" sub-key in Phase D1 (app/indexer/search/limits.go) without changing
// the spec's meaning.
//
// # Two independent mechanisms, deliberately not conflated
//
// [Acquire] paces ordinary traffic to a provider's configured
// requestsPerSecondMilli; it has no opinion on whether the provider is
// presently throttled. [State.Throttled] answers that from the error/backoff
// half. The fetch worker (F-5) is expected to check [Get] and skip a
// throttled provider entirely ("iterate providers by priority skipping
// throttled", spec §6.5) BEFORE ever calling [Acquire] for it -- conflating
// the two would make an hour-long throttle window also spend the rate
// limiter's tokens for no reason.
package throttle
