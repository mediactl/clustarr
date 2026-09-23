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

// Package usenet is grabarr's usenet engine (plan task D2-6): the Deployment
// replica D2-3's DownloadClient reconciler stands up for protocol=usenet. It
// consumes pkg/download/usenet (D2-2, commit dbdaf1b) -- the NNTP pool, yEnc
// assembly, PAR2 repair, unpack and the atomic rename into DataDir are all
// there already; this package's job is the seam between that client and the
// cluster: turning a DownloadClient into a Config, a Download's spec.source
// into an AddRequest payload, and a [download.Item] observation into
// Download.status telemetry under k8s.ManagerGrabarrEngine.
//
// # Two entry points
//
// [BuildClient] (in config.go) loads the DownloadClient this replica belongs
// to, resolves every provider's Secret and PostProcess's pointer defaults
// ([PostProcessFromSpec]), and builds the pkg/download/usenet.Client -- whose
// construction re-attaches every in-flight job synchronously before
// returning (see pkg/download/usenet.New's doc comment). That is what makes
// R4 satisfiable: a caller that gates engine readiness on BuildClient having
// returned cannot report ready before re-attach completes.
//
// [Reconciler] (in engine.go) then owns the Download side: for every Download
// labelled for this replica (downloadv1alpha1.LabelEngine ==
// "<client>-0" -- usenet clients are capped at one replica by
// DownloadClientSpec's own CEL rule, so the ordinal is always 0) it ensures a
// payload has been resolved and added, keeps status telemetry moving, and
// removes the transfer from the client once importarr finishes with it or
// the Download is deleted.
//
// # Wiring (task D2-8's job, not this one's)
//
//	dc := "<the DownloadClient this replica's --engine identity names>"
//	cl, _, err := usenetengine.BuildClient(ctx, mgr.GetClient(), o.Namespace, dc, o.DataDir, o.ScratchDir)
//	if err != nil {
//	    return err
//	}
//	// cl is re-attached at this point (see above) -- only now is it safe to
//	// let the readiness probe report healthy. defer cl.Close() belongs
//	// wherever the process's shutdown path already lives.
//	categories, err := usenetengine.LoadDownloadClient(ctx, mgr.GetClient(), o.Namespace, dc)
//	if err != nil {
//	    return err
//	}
//	r := &usenetengine.Reconciler{
//	    Client:   mgr.GetClient(),
//	    Download: cl,
//	    Resolver: &usenetengine.Resolver{RPC: bus},
//	    Recorder: mgr.GetEventRecorderFor("usenet-engine"),
//	    Engine:   o.Engine,
//	    Categories: categories.Spec.Categories,
//	}
//	if err := r.SetupWithManager(mgr); err != nil {
//	    return err
//	}
//
// # The PostProcess defaulting hazard (D2-2's carried finding)
//
// pkg/download/usenet.Config.PostProcess is a plain (non-pointer) struct;
// PostProcessSpec's three switches are *bool with
// +kubebuilder:default=true. An apiserver default fills a field ABSENT from
// submitted JSON and never touches a Go zero value directly, so treating a
// nil pointer as false -- Go's natural zero value -- would silently disable
// repair, unpacking and cleanup for every DownloadClient nobody has
// explicitly configured. [PostProcessFromSpec] resolves this explicitly:
// nil, and a &PostProcessSpec{} with every pointer nil, both mean "every
// switch on," and only an explicit non-nil pointer overrides its default.
// config_test.go proves both the nil and all-nil-pointer cases land on the
// CRD's true/true/true, and that an explicit false is honoured.
//
// # What this package deliberately does not do
//
// It never touches metadata.finalizers. [Reconciler.reconcileDeleting] calls
// [download.Client.Remove] once a Download carries a deletionTimestamp, but
// does not remove any finalizer -- finalizer bookkeeping is
// k8s.ManagerGrabarr's, on the Download controller (plan task D2-4), which
// does not exist in this tree yet. That leaves a genuine, unresolved seam:
// something must learn that this engine's Remove finished before the
// controller drops the finalizer, or a Download can be deleted while its
// scratch files are still being torn down. This package's contribution is
// making Remove happen and making it idempotent
// ([download.ErrNotFound] is treated as success, matching
// [download.Client.Remove]'s own contract); the coordination is D2-4's to
// design, and the report for this task says so explicitly rather than
// guessing at a mechanism (a status field, a condition, a bus event) that D2-4
// might choose differently.
//
// It never writes status.phase, status.conditions, status.engine or
// status.import -- see grabarr/status.go for the full field-manager split.
// grabarr/status.Patch itself refuses any manager but
// k8s.ManagerGrabarr/k8s.ManagerGrabarrEngine, so a caller that tried to
// route a status.import write through this package's Reconciler would be
// rejected by that package, not merely discouraged by this comment.
//
// # The three traps this package pays the same tax D2-5 does
//
//   - status.files is a listType=map; WithFiles APPENDS. [Reconciler.patchTelemetry]
//     assigns the whole apply configuration (*ac = *download.ApplyStatus(item))
//     rather than calling a With* method against the seed, so there is only
//     ever one file list per apply.
//   - A lost update is not an SSA release. [Reconciler.patchTelemetry] re-Gets
//     the Download immediately before applying, because payload resolution
//     (an RPC or a direct fetch) can be slow enough for another writer to have
//     moved status in between.
//   - An over-claim is silent: pkg/k8s.PatchStatus forces ownership, so a
//     double-claim never surfaces as a conflict. engine_envtest_test.go's
//     managedFields assertions are the only place that class of bug is
//     visible at all (CLAUDE.md; grabarr/controller/downloadclient's own
//     managedfields_envtest_test.go is the pattern this package's copies).
//
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloadclients,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
package usenet
