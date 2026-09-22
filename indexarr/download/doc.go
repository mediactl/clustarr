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

// Package download serves clustarr.rpc.indexarr.download: it fetches a
// release payload with the indexer's own session cookies and passkeys and
// hands grabarr either the bytes, a magnet link or a link to fetch itself.
//
// # The payload types are frozen
//
// DownloadRequest and DownloadResponse live in pkg/events/schema/index.go and
// are not this package's to extend. "DownloadResponse carries exactly one of
// Bytes, MagnetURL or RedirectURL" is their own doc comment, and every branch
// here sets exactly one -- or an Error and none.
//
// # The Error field is the whole error contract
//
// After a successful decode every failure is a populated
// DownloadResponse.Error; a transport error means the request never reached a
// handler. That asymmetry matters at both ends: too lenient and grabarr naks
// a dead tracker link five times and dead-letters it, too strict and the
// operator sees "download failed" with no reason. Handle therefore never
// returns an error, and the RPC wrapper is the only place a transport error
// for this verb is produced.
//
// # Status ownership
//
// This package writes Indexer.status as k8s.ManagerIndexarrWorker, through
// indexarr/status.Patch and nothing else. Server-side apply REPLACES a field
// manager's ownership set on every apply rather than merging into it, so an
// apply must declare all ten fields that manager owns even though this verb
// changes one:
//
//	escalationLevel  disabledUntil  initialFailureAt  lastFailureAt  lastFailure
//	queriesInWindow  grabsInWindow  lastRssAt  lastRssNewCount  indexedReleases
//
// status.WorkerFields is the single definition of that set (Ruling R14); this
// package calls it and never hand-rolls one. The apply is skipped entirely
// when the count did not change -- an apply that does not happen releases
// nothing, which is the one safe shortcut under a shared field manager.
//
// status.grabsInWindow is a PROJECTION of the KV ring; the ring is the source
// of truth. The search fan-out and the RSS worker also apply under this
// manager from their own possibly-stale cached read, so a lost update is
// possible. It self-heals at the next grab, and a CAS loop for a status field
// would be the wrong fix.
//
// # Grab accounting, and what it misses
//
// Ruling R3 puts grab counting here -- indexarr already holds the indexer's
// session and passkey at this point, and no new CLUSTARR_EVENTS consumer is
// needed. But a grab whose DownloadSource is torrentURL, magnetURL or nzbURL
// never calls this verb at all
// (api/download/v1alpha1/download_types.go:235), so those grabs go uncounted
// and status.grabsInWindow UNDERCOUNTS for public indexers. That is
// acceptable for M2 -- those grabs use no indexer credentials -- but
// grab-limit ENFORCEMENT cannot be built on this counter alone.
//
// # Redaction
//
// Nothing that reaches a log line, an error string or
// DownloadResponse.Error may carry a passkey. The stripping itself is
// pkg/cardigann's -- RedactURL and RedactErr are exported precisely so
// indexarr calls them (Ruling R26) rather than growing a second
// implementation that drifts. On top of that this package adds two bounds of
// its own: Fetcher.Scrub replaces the indexer's known secret VALUES, for the
// case where a third party echoed one back at us, and every message is
// truncated to maxErrorChars. A GUID is redacted like a URL before it reaches
// a log line, because a GUID is very often the release's details URL, and it
// never reaches a metric label at all.
//
// # The download URL is never rewritten
//
// The link came out of the indexer's own feed and already embeds whatever
// credential that indexer signs with: apikey, passkey, rsskey, a one-time
// token. Appending our own apikey can duplicate a parameter, break an HMAC or
// produce a 403 that reads like an auth failure. Credentials are applied only
// as cookies on a per-origin jar, plus spec.timeout, the per-host limiter and
// the redirect policy.
//
// One consequence: an empty DownloadRequest.URL is a hard Error.
// relindex.Query has no GUID field and ADR-0003 fixes the Store at four
// methods, so indexarr cannot resolve a GUID to a URL. Carried item.
//
// # Wiring (Task D1-8, in indexarr/run.go)
//
//	dl := &download.Service{Client: mgr.GetClient(), Bus: bus,
//	    Fetch: download.NewFetcherFor(mgr.GetClient(), limiters)}
//	q  := &query.Service{Store: store}
//	svc := &search.Service{..., Download: dl.Handle, Query: q.Handle}
//
// `limiters` is the ONE *ratelimit.Limiter D1-3 constructs and shares with the
// fan-out and the RSS worker, so all three pace against the same per-host
// buckets. This package never constructs one.
//
// # RBAC
//
// The markers below are package-level on purpose: controller-gen collects
// RBAC only from package-level comments and silently ignores one attached to
// a function -- and envtest does not enforce RBAC, so a misplaced marker
// passes every test and fails only on a real cluster.
//
// This verb reads Indexer, reads the Secrets that hold the indexer's
// credentials and its login session, and writes only the /status subresource.
// Each pair is already declared by indexarr/status and by the Indexer
// reconciler; they are restated here so the package's own needs survive
// either of those moving, and controller-gen deduplicates them.
//
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=get;list;watch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
package download

import (
	"context"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Handle must stay assignable to the search service's DownloadFn. The
// signature is restated rather than imported: a named func type accepts a
// plain func of the same signature, and importing indexarr/search would
// couple two packages that have no other reason to know about each other.
var _ func(context.Context, schema.DownloadRequest) schema.DownloadResponse = (&Service{}).Handle
