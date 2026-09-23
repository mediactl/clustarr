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

// Package downloadclient reconciles DownloadClient: it stands up the engine
// workload the client describes, reports DiskSpaceOK/EngineReady/Ready, and
// sweeps expired blocklist entries. Design spec §6.3, §4.4; plan task D2-3.
//
// # Two reconcilers, one package
//
// [Reconciler] owns DownloadClient itself -- the StatefulSet (torrent) or
// Deployment (usenet) it describes, and DownloadClientStatus. [BlocklistSweeper]
// owns nothing on DownloadClient at all; it watches Download and deletes the
// ones whose blocklist has expired. They are two controllers, registered
// separately, because they watch different root kinds and controller-runtime
// reconciles one kind per controller. Both need wiring in grabarr/run.go's
// setupControllers, which is task D2-8's job, not this one's:
//
//	if err := downloadclient.NewReconciler(
//	    mgr.GetClient(), mgr.GetEventRecorder("downloadclient"), o.DataDir, engineImage,
//	).SetupWithManager(mgr); err != nil {
//	    return err
//	}
//	if err := downloadclient.NewBlocklistSweeper(
//	    mgr.GetClient(), mgr.GetEventRecorder("downloadclient-blocklist"),
//	).SetupWithManager(mgr); err != nil {
//	    return err
//	}
//
// engineImage is CLUSTARR_ENGINE_IMAGE (config/manager/grabarr.yaml already
// declares it, on the grabarr Deployment, as "images the controller stamps
// into the engine workloads it owns"); no code reads that env var into
// grabarr.Options yet, because grabarr/run.go is outside this task's directory
// (see the task instructions: stay inside
// grabarr/controller/downloadclient/). D2-8 adds the flag/Options field and
// passes it through.
//
// # Why the sweep deletes rather than clears BlocklistedUntil
//
// download_types.go's own doc comment on LabelBlocklisted settles this:
// "grabarr sweeps expired entries, and the decision engine reads the live set
// through a catalogarr informer" and BlocklistedUntil's comment is more
// direct still -- "grabarr deletes the Download once the deadline passes."
// [BlocklistSweeper] therefore never calls grabarr/status.Patch or claims any
// part of k8s.ManagerGrabarr's Download.status set: it Gets, checks the label
// and the deadline, and either client.Delete()s or requeues for the moment
// the deadline arrives. There is no status write here to build a partial
// declaration of in the first place -- the early-return hazard CLAUDE.md
// warns about needs an apply to exist, and this reconciler never issues one
// against Download.
//
// # DiskSpaceOK has no spec field to read a floor from
//
// Unlike RootFolderSpec.MinFreeBytes, DownloadClientSpec carries no
// minimum-free-space field (verified against downloadclient_types.go). This
// package therefore invents one -- [DefaultMinFreeBytes], overridable on
// [Reconciler] -- the same way catalogarr/controller/rootfolder invented its
// recheckInterval: cheap, frequent enough, and documented so a later task can
// replace it with a real signal (a RootFolder-style spec field, or a cheaper
// kubelet volume metric) without archaeology.
//
// # What this package deliberately does not build
//
// No Service and no hostPort for the torrent engine's peer-facing port
// (spec §6.3 lists "hostPort/Service for ListenPort" as part of the torrent
// engine's eventual shape). Peer connectivity is a networking concern for
// whichever task wires the engine's actual listener; standing up a Service
// with no engine behind it yet would be dead configuration. The container
// port is still declared on the pod so that task has something to point a
// Service at.
//
// No per-provider Secret validation for usenet clients (unlike
// indexerproxy's existence check on spec.secretRef). DiskSpaceOK and
// EngineReady are the two conditions this task owns; a client whose
// NNTPProvider secrets are wrong will simply fail to connect once an engine
// exists to try, and that failure belongs to the engine (D2-2/D2-6), not to
// this reconciler.
//
// # R5 -- this task turns automatic search on
//
// catalogarr/worker/search/worker.go's enabledProtocols already lists live
// DownloadClient objects and fails closed for a protocol with no enabled
// client (verified at worker.go:498-527). Nothing in this package changes
// that code, and nothing needs to: creating a DownloadClient CR was already
// possible before this task landed, but pointless, because nothing reconciled
// it. The moment a reconciler exists to make a DownloadClient mean something
// -- a real engine workload, a real Ready condition -- the first enabled
// client an operator or an e2e fixture creates flips enabledProtocols() from
// empty to non-empty for that protocol, and automatic search, previously
// inert, starts grabbing.
//
// # The RBAC markers below are package-level comments
//
// controller-gen only collects +kubebuilder:rbac from a comment group that is
// not attached to a declaration -- a blank line above the `package` clause
// keeps this one package-level. cmd/clustarr's TestRBACMarkersArePackageLevel
// is the guard; four catalogarr controllers shipped with none of their
// permissions reached because their markers sat directly above
// SetupWithManager instead.
//
// grabarr/status/doc.go already grants downloads and downloads/status
// get;list;watch;update;patch for the Download controller (D2-4) and the
// engines; this package adds delete on downloads, for the sweep, and get,
// list, watch, update and patch are re-declared here too because this
// package's own Reconcile lists Downloads for the Active/Queued/Seeding
// rollup -- a marker states what the package it sits in does, not what is
// already granted somewhere else in the tree, so a working permission set
// does not depend on two packages happening to land in the right order.
//
// config/rbac/role.yaml is NOT regenerated by this task -- Makefile's
// RBAC_DIRS already lists grabarr, so `make manifests` will pick these up,
// but running it is task D2-8's job (see the task instructions). Until then
// cmd/clustarr's TestGeneratedRoleCoversEveryStatusWriter is expected to fail
// on download.clustarr.io/downloadclients/status, exactly as
// grabarr/status/doc.go's own comment predicts for downloadclients, the
// engine StatefulSet and the blocklist sweep.
//
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloadclients,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloadclients/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
//
// The usenet engine's provider Secrets are read by name (get) to fold their
// data into the engine's config hash, and watched (list, watch) through a
// cache filtered to Secrets labelled download.clustarr.io/watch=enabled, so a
// rotation restarts the engine at once. RBAC cannot narrow list and watch by
// label, so the filter bounds what the controller holds rather than what it
// may ask for; the unfiltered alternative would cache every Secret in the
// cluster.
//
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
package downloadclient
