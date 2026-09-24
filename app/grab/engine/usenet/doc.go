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
// # Wiring
//
// app/grab/run.go's setupUsenetEngine is the real wiring; in outline:
//
//	// A direct (uncached) client: this runs before mgr.Start, and BuildClient
//	// also reads the providers' Secrets through it.
//	cl, dc, err := usenetengine.BuildClient(ctx, direct, o.Namespace, clientName, o.DataDir, o.ScratchDir)
//	if err != nil {
//	    return err
//	}
//	// cl is re-attached at this point (see above) -- only now is it safe to
//	// let the readiness probe report healthy.
//	r := &usenetengine.Reconciler{
//	    Client:     mgr.GetClient(),
//	    Download:   cl,
//	    Resolver:   &usenetengine.Resolver{RPC: bus},
//	    Recorder:   mgr.GetEventRecorder("usenet-engine"),
//	    Engine:     o.Engine,
//	    Categories: dc.Spec.Categories,
//	}
//	if err := r.SetupWithManager(mgr); err != nil {
//	    return err
//	}
//	// plus a usenetengine.Reaper registered with mgr.Add, and cl.Close on
//	// the process's shutdown path.
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
// # The engine finalizer (ruling R-6)
//
// [Reconciler] adds app/grab/engine's [engine.Finalizer] to every Download
// labelled for this replica before it adds the transfer, and on deletion
// removes the transfer ([download.Client.Remove] stops the job's fetch
// goroutines, discards its scratch job and, per spec.removeDataOnDelete,
// the published content) and only then drops the finalizer. The Download
// controller's own removeDataOnDelete finalizer waits for this one, which
// closes the ordering race Phase D2 carried: the controller no longer
// removes files a job still has open. app/grab/engine's package doc has the
// whole protocol, including the bounded timeout after which the controller
// stops waiting for an engine that is gone.
//
// [Reaper] (reaper.go) is the backstop for exactly that timeout: a transfer
// this client re-attaches after the controller dropped the finalizer on its
// behalf has no Download left, and the reaper -- a level-driven pass that
// lists this replica's client transfers against its Downloads -- removes it
// once it is older than the grace period, by the job's own persisted age.
//
// # What this package deliberately does not do
//
// It never writes status.phase, status.conditions, status.engine or
// status.import -- see app/grab/status.go for the full field-manager split.
// app/grab/status.Patch itself refuses any manager but
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
//     visible at all (CLAUDE.md; app/grab/controller/downloadclient's own
//     managedfields_envtest_test.go is the pattern this package's copies).
//
// The engine finalizer (above) is why this package updates Downloads and
// their finalizers subresource.
//
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloadclients,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
package usenet
