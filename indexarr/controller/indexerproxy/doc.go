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

// Package indexerproxy reconciles IndexerProxy: it checks that the proxy is
// addressable and reachable, and reports Ready with when it last looked and
// what version answered.
//
// # What this controller does NOT do
//
// It does not route any indexer request through the proxy, does not build a
// transport for one, does not read the credentials in spec.secretRef (it only
// checks that the Secret exists), and does not evaluate spec.selector against
// any Indexer. Applying a proxy to an indexer's requests -- including the
// "at most one FlareSolverr matches, applied last" rule in the CRD -- is M6
// (Phase G), together with the FlareSolverr client itself. What happens here
// is a reachability probe and nothing else.
//
// # Wiring (Task D1-8)
//
// The exact call indexarr/run.go must make:
//
//	if err := indexerproxy.NewReconciler(
//	    mgr.GetClient(),
//	    mgr.GetEventRecorderFor("indexerproxy"), // record.EventRecorder, core/v1 Events
//	    httpClient,                              // nil is accepted: http.DefaultClient
//	).SetupWithManager(mgr); err != nil {
//	    return err
//	}
//
// IndexerProxy is namespaced, so the apply configuration takes name and
// namespace.
//
// # The owned field set
//
// IndexerProxy.status has exactly one writer, k8s.ManagerIndexarr, so the
// two-manager split in indexarr/status (which covers Indexer, not this kind)
// does not apply. What does apply is the rule behind it: server-side apply
// REPLACES a field manager's ownership set on every apply rather than merging
// into it, so a field this manager sent before and omits now is released,
// which reads as "reset to zero" on the object. Phase C hit that eight times,
// three of them on an early-return path that built a conditions-only apply --
// and an early return is almost always the transient path, so a healthy
// object gets gutted by a blip.
//
// Every apply this package makes therefore declares all of:
//
//	status.observedGeneration
//	status.conditions      (Ready; the CRD defines no other condition type)
//	status.lastCheckedAt   (see the shape exception below)
//	status.version
//
// and does so from ONE place, statusFor, seeded from the live status by
// probeStateFrom. There is no second apply and no partial apply on any path,
// including both early returns (an unaddressable spec, and a spec.secretRef
// that names a Secret which is not there yet).
//
// The one shape exception: status.lastCheckedAt is a *metav1.Time rendered as
// an RFC3339 string, and the zero Time marshals to null, which the CRD's
// format: date-time rejects. It is therefore omitted while nil -- which is
// only ever true before the first probe, never because one failed. A failed
// probe still stamps it; "when the proxy was last probed" is not "when it was
// last reachable".
//
// status.version is sent on every apply, including an empty one for a SOCKS
// proxy, which reports no version by design. It is overwritten only when a
// probe returns a non-empty version, so a FlareSolverr that stops answering
// keeps its last known version next to a Ready=False rather than losing it.
//
// The RBAC markers below are package-level comments, separated from the
// package clause by a blank line. controller-gen collects +kubebuilder:rbac
// only from package-level comments and silently ignores one attached to a
// declaration; in Phase C that meant four controllers' rules never reached
// config/rbac/role.yaml, and no envtest could see it because envtest does not
// enforce RBAC. cmd/clustarr's TestRBACMarkersArePackageLevel is the guard.
//
// The Events group is "" (core/v1), not events.k8s.io: the recorder here comes
// from mgr.GetEventRecorderFor, which returns a
// k8s.io/client-go/tools/record.EventRecorder and writes core Events.
//
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexerproxies,verbs=get;list;watch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexerproxies/status,verbs=get;update;patch
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
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
package indexerproxy
