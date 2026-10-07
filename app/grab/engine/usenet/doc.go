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

// Package usenet is grabarr's usenet engine (ADR-0019 §6.7): the
// engine.Transfers over its embedded client -- the usenet pipeline and its scratch manifests -- which the shared
// command handler (app/grab/engine) drives with the manager's seq-fenced
// desired-state commands. It holds each transfer's journal (the claim,
// the last applied seq, the import) beside its own state, reads no
// catalog object and writes no Kubernetes object: the Download
// reconciler, the orphan reaper and the engine finalizer are gone, their
// decisions the manager's (app/grab/lifecycle).
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
// # RBAC
//
// The engine reads its DownloadClient and its Secrets (the providers') by
// name at start; it writes nothing (§9.1).
//
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloadclients,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
package usenet
