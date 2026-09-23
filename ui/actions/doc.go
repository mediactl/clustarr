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

// Package actions is the only package under ui/ that writes to the cluster,
// and it writes exactly what design amendment §A3.2 lets the UI write:
//
//   - "search now" creates a Search ([SearchNow]);
//   - "rescan" creates a LibraryScan ([Rescan]);
//   - "monitor this" patches a catalog item's spec.monitored ([SetMonitored]).
//
// Nothing else. The UI never writes status, holds no status field manager and
// owns no CRD (§A3.2, CLAUDE.md), so every action either creates a
// short-lived request object or patches one spec leaf -- and anything the UI
// can do here, `kubectl create` or `kubectl patch` can do too.
//
// # How narrow, and what enforces it
//
// Each action is a plain function over the narrowest interface that can
// perform it: [Creator] (Create only) for the two creates, [Patcher] (Patch
// only) for the spec patch. [Writer] is the two together, and [Actions]
// holds one unexported so the rest of ui/ can reach the three actions and
// never the Create/Patch methods behind them. ui/server.go's Options.Reader
// stays a client.Reader; the writer arrives through Options.Actions, a
// separate field, so the read path is read-only by type.
//
// Three guards hold that shape (ruling R2,
// docs/superpowers/plans/2026-09-23-phase-g-parity.md):
//
//   - ui/guard_test.go's TestUINeverWrites allows Create and Patch calls in
//     this package and nowhere else under ui/, bans Status, SubResource and
//     every *Status write helper everywhere -- this package included -- and
//     bans Update, Delete, DeleteAllOf and Apply everywhere, because the
//     role below grants no verb that would let them succeed.
//   - cmd/clustarr/ui_rbac_test.go holds config/rbac/ui_role.yaml to reads
//     plus exactly [Grants]: create on searches and libraryscans, patch on
//     the ten catalog kinds that have a spec.monitored, no */status
//     resource, no update, no delete.
//   - this package's envtest performs every action against a real apiserver
//     and asserts on metadata.managedFields that [FieldManager] never
//     appears on a status path, owns exactly spec.monitored after a patch,
//     and that the (group, resource, verb) each action actually hit is
//     exactly [Grants].
//
// # Why "monitor this" is a JSON merge patch and not server-side apply
//
// Every other writer in Clustarr applies (pkg/k8s.Apply/PatchStatus). This
// one deliberately does not:
//
//  1. Server-side apply treats each apply as the manager's complete
//     declaration and releases whatever that manager sent before and omits
//     now (CLAUDE.md, "Gotchas found the hard way"). A controller asserts one
//     computed desired state per object, so that is the right model for it.
//     The UI is the opposite: a stream of independent, one-field human edits
//     under one manager. The moment a second UI edit lands on the same
//     object -- G3-4's settings forms patch spec through this package too --
//     an apply of field B would release field A, the "two components sharing
//     one field manager" failure CLAUDE.md records. A merge patch is recorded
//     as an Update-operation manager, whose owned set accumulates across
//     patches and is never released by omission.
//  2. A merge patch to an object that does not exist is a 404; an apply to
//     one is a create. A stale page toggling a Movie that was deleted a
//     second ago must fail, not resurrect a stub with only spec.monitored
//     set. RBAC would refuse that create in a real cluster (the role grants
//     no create on catalog kinds), but envtest does not enforce RBAC, and
//     behaviour that differs between the test and the cluster is exactly how
//     a misplaced rbac marker passes every test in this tree.
//  3. The controllers that also set spec.monitored do so only at creation:
//     the Series fan-out creates each Episode with r.Create and never sends
//     spec.monitored again, so "after that it belongs to the user"
//     (catalogarr/controller/series/fanout.go, DesiredEpisode.Monitored, and
//     reconciler.go's ensureEpisode). A create is itself an Update-operation
//     entry in managedFields, so there is no applier whose next apply could
//     release the leaf out from under the user. The UI's merge patch takes
//     that one leaf over from the create-time manager -- an update never
//     conflicts -- which is precisely the audit trail R2 asks for:
//     managedFields shows [FieldManager] owning f:spec.f:monitored and
//     nothing else. Should a controller ever start force-applying
//     spec.monitored (pkg/k8s always forces), it would take the leaf back
//     from the UI on its next apply whichever of the two write shapes the UI
//     used, so that risk does not favour apply either.
//
// The patch body is built from a typed struct and carries only
// {"spec":{"monitored":<bool>}}. It needs no resourceVersion precondition:
// it sets one leaf to the user's chosen value, so there is no read to go
// stale between the click and the write.
//
// # Why the creates use generateName, and label what they make
//
// A Search or LibraryScan requested from the UI is a fresh request each
// time, not a desired state to converge on, so it gets an apiserver-generated
// name ([metav1.ObjectMeta.GenerateName]) rather than a deterministic one;
// server-side apply cannot create with generateName at all, which is the
// other reason these are plain creates. Each carries [LabelOrigin]=
// [OriginUI] so `kubectl get searches,libraryscans -l clustarr.io/origin=ui`
// separates user-requested work from the wanted cron's and the RootFolder
// schedule's. The LibraryScan deliberately does not carry
// importarr/controller/rootfolderschedule's LabelRootFolder: that label is
// how the schedule finds the scans it created itself.
//
// # The field manager
//
// Every write here is made as [FieldManager], "clustarr-ui" -- pkg/k8s's
// ManagerUI, restated rather than imported. ui/ does not import pkg/k8s at
// all (TestUINeverWrites bans it, this package included), for the reason
// ui/reader.go restates its scheme list: pkg/k8s is where PatchStatus lives,
// and keeping the whole package out of ui/ is a stronger guarantee than
// allowing it and policing which identifiers get used. actions_test.go pins
// the two strings together.
package actions
