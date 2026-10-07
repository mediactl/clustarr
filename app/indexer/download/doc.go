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
// None. This verb writes no status. status.grabsInWindow is the Indexer
// reconciler's projection of the grab ring in clustarr-indexer-limits
// (app/indexer/status's package doc, since 2026-10-01): it reads the ring on
// every pass and is woken by every ring change, so the count follows the
// grabs without a status write here -- and without the lost-update and
// release hazards a third writer under the worker manager kept bringing.
//
// # Grab accounting, and spec.limits.grabLimit
//
// Every grab reserves on the ring (app/indexer/limits.ReserveGrab) BEFORE
// the indexer is asked, and the reservation is refused once the window holds
// spec.limits.grabLimit: the reply is then an Error that limits.GrabLimited
// recognises, naming when the window next has room, and the indexer is never
// contacted. The ring is keyed by GUID, so a grab already in the window
// passes without counting again -- catalogarr's grab path reserves the same
// GUID before it creates the Download whose engine calls this verb, which is
// where a limit has to hold for an automatic grab. A grab that then does not
// happen (the fetch failed, or the link named another host) gives back the
// slot this call took.
//
// A grab whose DownloadSource is torrentURL, magnetURL or nzbURL never calls
// this verb at all (grabarr fetches it directly; see api/download/v1alpha1's
// DownloadSource), so directgrab.Reconciler
// (app/indexer/controller/directgrab) counts those from the Download's
// creation instead, into the same ring, never refused -- it has
// already happened. Again the GUID key means no grab is counted twice.
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
// the redirect policy -- and through the Indexer's IndexerProxies
// (app/indexer/proxy), like every other request it makes.
//
// One consequence: an empty DownloadRequest.URL is a hard Error.
// relindex.Query has no GUID field and ADR-0003 fixes the Store at four
// methods, so indexarr cannot resolve a GUID to a URL. Carried item.
//
// # Wiring (Task D1-8, in app/indexer/run.go)
//
//	dl := &download.Service{Client: mgr.GetClient(), Bus: bus,
//	    Fetch: download.NewFetcherFor(mgr.GetClient(), limiters)}
//	q  := &query.Service{Store: store}
//	svc := &search.Service{..., Download: dl.Handle, Query: q.Handle}
//
// Bus is the clustarr-indexer-limits grab ring as well as the RPC transport;
// without it there is no grab accounting and no grab limit.
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
// This verb reads Indexer and the Secrets that hold the indexer's
// credentials and its login session, and writes nothing in Kubernetes. The
// read is already declared by the Indexer reconciler; it is restated here so
// the package's own needs survive that moving, and controller-gen
// deduplicates it.
//
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=get;list;watch
//
// secrets is get ONLY, not get;list;watch. indexarr.Options.ManagerOptions
// disables the Secret cache (client.CacheOptions.DisableFor), so every Secret
// read here is a live single-object Get and nothing in indexarr ever Lists or
// Watches one.
//
// Be precise about what this buys, because it is less than it looks: Clustarr
// generates ONE clustarr-manager-role and binds it to every service's
// ServiceAccount, and catalogarr's metadata gateway reads Secrets through a
// CACHED client, so it genuinely needs list;watch and the union keeps them in
// the generated Role. indexarr's pod is therefore still granted verbs it does
// not use. What this marker fixes is the declaration -- the package asks for
// what it uses, so the day the role is split per service the narrowing is
// already recorded. Splitting it is the real fix and is not this task's.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
package download

import (
	"context"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Handle must stay assignable to the search service's DownloadFn. The
// signature is restated rather than imported: a named func type accepts a
// plain func of the same signature, and importing app/indexer/search would
// couple two packages that have no other reason to know about each other.
var _ func(context.Context, schema.DownloadRequest) schema.DownloadResponse = (&Service{}).Handle
