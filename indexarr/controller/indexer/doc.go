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

// Package indexer reconciles index.clustarr.io Indexer objects: it validates
// the spec, probes Torznab caps, resolves the protocol, the privacy class and
// the session-Secret reference, and derives the Ready, Authenticated, Healthy
// and RateLimited conditions.
//
// The health and backoff ladder -- RecordFailure, RecordSuccess, Healthy,
// StartupGrace, EscalationTable -- is NOT here. It lives in indexarr/status,
// beside the declaration of the very fields it computes (ruling R35). It
// moved because indexarr's RSS poll, search fan-out and download verb all
// need it while this package imports indexarr/status, so a ladder here could
// never share a home with indexarr/status.ApplyEscalation, the one mapping
// from an Escalation onto an apply. This package only READS the result, to
// derive the Healthy condition and the requeue delay.
//
// [status.SupportsMode] followed it there for the same reason (ruling R39):
// this package WRITES status.caps, but asking "does this indexer advertise
// this mode?" is a read-only predicate every consumer needs, and the search
// fan-out was importing a CONTROLLER to gate its candidates on it. projectCaps
// and the predicate are still held together by
// TestModeVocabularyIsTorznabsWireValues, which projects real caps here and
// asserts through the predicate there.
//
// # Field-manager split (design spec §2, Phase D1 rulings R6 and R31)
//
// Two writers reach IndexerStatus and each owns a disjoint set, because
// server-side apply REPLACES a manager's ownership set on every apply rather
// than merging it -- one shared name means each writer silently releases the
// other's fields.
//
//	k8s.ManagerIndexarr ("indexarr", this package):
//	    observedGeneration, conditions, protocol, privacy, caps,
//	    sessionSecretRef
//	k8s.ManagerIndexarrWorker ("indexarr-worker", indexarr's RSS poll and
//	search fan-out):
//	    escalationLevel, disabledUntil, initialFailureAt, lastFailureAt,
//	    lastFailure, queriesInWindow, grabsInWindow, lastRssAt,
//	    lastRssNewCount, indexedReleases
//
// This package never applies as ManagerIndexarrWorker. It reads the worker's
// fields to derive conditions and a requeue delay, nothing more.
//
// # One declaration of the owned set, and it is not here (ruling R31)
//
// The table above is documentation. The single machine-readable declaration
// of what k8s.ManagerIndexarr owns lives in indexarr/status.ControllerFields,
// and every apply this package makes goes through indexarr/status.Patch,
// which seeds the apply configuration from the live status and then runs this
// package's mutate. Two places declaring one manager's owned set is precisely
// how the two drift apart, silently, which is the release bug in its ninth
// form; Phase C found eight.
//
// The consequences for the code here are worth stating, because they are why
// Reconcile looks the way it does:
//
//   - Reconcile resolves the owned fields onto its LOCAL copy of idx.Status
//     before it applies. ControllerFields then seeds from that copy, so the
//     apply is a complete declaration by construction rather than by a
//     remembered discipline at seven return sites.
//   - There is exactly one call site, [Reconciler.patch], and every return in
//     Reconcile goes through it. An early return therefore re-sends the caps,
//     protocol and privacy already on the object instead of releasing them.
//     Phase C's most expensive SSA defect was an early-return path that built
//     a partial status and wiped the happy path's work -- and an early return
//     is always the transient path, so a healthy object got gutted by a blip.
//   - Conditions are set in exactly ONE place per apply. ControllerFields
//     deliberately does not seed them (this reconciler is their only writer
//     and derives all four every pass) and the generated WithConditions
//     APPENDS rather than replaces, so setting them in both the seed and the
//     mutate is rejected outright with `duplicate entries for key
//     [type="Ready"]`.
//
// The owned set may vary with what the Indexer has ever resolved -- until a
// readable Secret (spec.generic) or a loaded definition has produced
// status.protocol it is empty, and the CRD's enum [torrent, usenet] makes
// sending "" an apiserver rejection, so it is omitted. It must never vary
// with a transient OUTCOME.
//
// # This reconciler never writes an escalation field
//
// escalationLevel, disabledUntil, initialFailureAt, lastFailureAt,
// lastFailure, queriesInWindow, grabsInWindow, lastRssAt, lastRssNewCount and
// indexedReleases belong to indexarr-worker. This package READS them (to
// derive the Healthy and RateLimited conditions and to choose a requeue
// delay); the workers compute the next set with indexarr/status's
// RecordFailure/RecordSuccess and apply it themselves. A
// caps-probe failure therefore moves conditions and the requeue delay and
// does not move escalationLevel: writing the escalation set here under
// indexarr-worker would release the counters this reconciler does not know.
//
// # Cardigann-defined Indexers (Phase G, G1-1)
//
// spec.definitionRef names an IndexerDefinition; spec.definition names a
// bundled id, which -- because the bundled corpus is not shipped yet --
// resolves only through an IndexerDefinition that declares it (spec.replaces,
// then status.id). The definition supplies status.caps (modes renamed to the
// Torznab wire values), status.protocol (torrent) and status.privacy (with
// the schema's "semi-private" mapped to the CRD's "semiPrivate"); no caps
// probe runs. The login is the probe: a session-producing login (form, post,
// cookie) runs when the session is missing or near expiry and is persisted
// by [SessionStore] into the clustarr-indexer-sessions KV bucket (key
// [SessionKey]) and the owned Secret named by status.sessionSecretRef; a
// get/oneurl login runs on the caps-probe cadence to prove the credentials.
//
// Searching is NOT done here. [ClientCache.For] builds the Cardigann engine
// adapter behind the same [Client] interface a *torznab.Client satisfies, so
// the search fan-out, the RSS poll and -- through
// [ClientCache.DefinitionFetcherFor] -- the download verb all drive it
// through their existing seams (ruling R5), and a tracker's search.error
// page is an error the fan-out escalates rather than zero results (R6).
//
// spec.proxyRef is applied, for http and socks5 proxies, by the one builder
// every path shares; socks4 and flaresolverr are refused rather than
// bypassed. IndexerProxy.spec.selector matching is NOT implemented.
//
// # What this controller does NOT do
//
// No domain events -- §8.2's indexer.disabled|recovered|limited fire where
// the escalation transition is applied, which is the worker, not here.
//
// It DOES seed the RSS poll chain, and that is the only thing it publishes
// (ruling R36). This sentence previously read "No RSS scheduling (that is the
// RSS worker's WithScheduleAt)" while rss.ScheduleNext's own doc said the
// reconciler seeded the first one; neither did, so nothing ever started a
// chain and the release firehose never ran in a real cluster. The division is
// now: this reconciler seeds a poll for an enabled, healthy Indexer at
// rss.NextPollAt on every pass, and the worker schedules the next one at the
// end of every poll. The seed is idempotent by msg-id rather than by memory --
// see [Reconciler.seedRSSSchedule].
//
// The RBAC markers below are package-level comments, separated from the
// package clause by a blank line. controller-gen collects +kubebuilder:rbac
// only from package-level comments and silently ignores one attached to a
// declaration; in Phase C that meant four controllers' rules never reached
// config/rbac/role.yaml, and no envtest could see it because envtest does not
// enforce RBAC. cmd/clustarr's TestRBACMarkersArePackageLevel is the guard.
//
// indexers/status is deliberately NOT granted here. indexarr/status/doc.go
// carries it, on the package that actually performs the write, which is the
// convention D1-0 set and the same reasoning as R31: one declaration, in the
// place that does the thing.
//
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not. The marker and the recorder
// type move together or not at all: a mismatch is denied only on a real
// cluster, and no suite can see it, because envtest does not enforce RBAC.
// catalogarr's setupControllers records the occasion this repo learned it.
//
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=get;list;watch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexerdefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexerproxies,verbs=get;list;watch
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
//
// create and patch are for the owned session Secret alone: SessionStore.Save
// server-side applies it, and an apply that creates needs both verbs.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
package indexer
