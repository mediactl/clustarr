# Container search — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans.

**Goal:** a Search on an Author, Artist or Comic fans out into one auto-grabbing child Search per monitored child.

**Architecture:** new `spec.grabBest` and `status.children` on Search; the Search controller fans out and aggregates; `handleGrabs` adds the best approved result when `grabBest`; the UI's container button creates the parent with `grabBest`.

**Tech Stack:** Go, controller-runtime, templ.

**Spec:** `docs/superpowers/specs/2026-09-30-container-search-design.md`

## Global Constraints

- Gate: `env -u KUBEBUILDER_ASSETS -u CLUSTARR_PG_ASSETS go test ./...` and `make lint`; `make generate manifests` after the API change; pathspec commits; no push from a task.
- Every status apply declares the manager's whole set (`newStatusUpdate`/`apply`); `status.children` and a container's `finishedAt` join it.
- `grabBest` is a plain bool (default false: Go's zero is the default, no trap).

## Review Focus

1. A repeat reconcile of a Running parent creates no second child and re-grabs nothing.
2. A child's TTL never deletes it under a live parent.
3. A parent with 0 monitored children, and one over the cap.
4. `grabBest` on a result list whose best is rejected (temporary) grabs nothing.
5. The apply on a parent never releases `grabbed`, `phase`, `startedAt` or `children`.

### Task 1: API
Files: `api/catalog/v1alpha1/search_types.go` (+generated, CRD). `SearchSpec.GrabBest bool`, `SearchStatus.Children *SearchChildren`, `SearchChildren{Total,Running,Completed,Failed,Grabbed int32}`, `LabelParentSearch = "catalog.clustarr.io/parent-search"`, `ContainerKind(kind) bool`. Test: `TestContainerKind`.

### Task 2: grabBest
Files: `app/catalog/controller/search/reconciler.go`, `grab.go`, tests. `grabGUIDs(s)` = spec.grab plus the first approved result's GUID when `grabBest`; `grabsPending` and `handleGrabs` iterate it; the auto GUID's Download gets `grabbedBy: search`, `manual: false`. Tests: approved best grabbed once; rejected-only grabs nothing; a second reconcile creates nothing new.

### Task 3: fan-out and aggregation
Files: `app/catalog/controller/search/container.go` (+test), `reconciler.go` (Reconcile switch, `SetupWithManager` Owns, `ttlDeadline` child exemption, apply of `children`/`finishedAt`). Tests: monitored-only fan-out, idempotent; counts/phase; cap; zero children; child never self-expires.

### Task 4: UI
Files: `ui/actions/actions.go` (`SearchNow` sets `GrabBest` for a container kind), `ui/views/detail.templ` (button label), tests.
