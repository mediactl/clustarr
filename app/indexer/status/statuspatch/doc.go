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

// Package statuspatch is the one writer of Indexer.status: [Patch] and
// [PatchCAS] apply the complete set app/indexer/status declares for a
// field manager (ControllerFields for k8s.ManagerIndex, WorkerFields for
// k8s.ManagerIndexWorker).
//
// It was split out of app/indexer/status (ADR-0019 A6.3) because the index
// agent links that package for the read-only predicates Healthy and
// SupportsMode, the ladder's event producer and the declarations, and a
// package an agent reaches must hold no Kubernetes writer (§9.2). Since
// A6.3 only the manager calls this package: the Indexer controller applies
// its own set under k8s.ManagerIndex and incorporates the index agent's
// clustarr-indexer-health records into WorkerFields under
// k8s.ManagerIndexWorker (ruling R19). The agent reports outcomes and never
// writes status.
//
// The RBAC markers live here, on the package that performs the write. They
// are package-level comments on purpose: controller-gen collects markers
// only from package-level comments and silently ignores one attached to a
// function.
//
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=get;list;watch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers/status,verbs=get;update;patch
package statuspatch
