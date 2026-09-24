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

// Package bundle loads a Cardigann definition bundle into the cluster as
// IndexerDefinitions.
//
// Two bundles reach it. By default it is the corpus compiled into the
// binary: Prowlarr's Cardigann definitions, added to the project by its
// owner on 2026-09-23 and embedded as a deflated zip
// (app/indexer/bundle/embedded, --cardigann-bundled, default true). That
// supersedes gap-fix ruling R-13, under which Clustarr shipped no corpus;
// hack/sync-cardigann's package doc keeps the upstream licensing record R-13
// was decided on. Otherwise it is an operator's directory, which
// --cardigann-definitions-dir names and which replaces the embedded corpus
// (mounted from a ConfigMap, a PVC or an init container, for example what
// hack/sync-cardigann writes). Either way indexarr runs this loader at
// startup, and every
// definition cardigann.LoadBundle accepts becomes an IndexerDefinition named
// cardigann.ObjectName(id), so an Indexer's spec.definition: <id> resolves
// through it -- the indexer controller matches a definition id against
// IndexerDefinition status.id -- with no manifest to apply by hand.
//
// # Whose object is it
//
// A bundled IndexerDefinition carries the label [LabelBundled]. The loader
// creates a missing one and updates only its own: an IndexerDefinition of
// the same name WITHOUT the label is the operator's -- applied from
// hack/sync-cardigann's manifests, or written by hand -- and is left alone,
// with a log line. An operator who wants to change a bundled definition
// creates a new one with spec.replaces instead, which the indexer
// controller prefers (see IndexerDefinitionSpec.Replaces).
//
// Writes go under k8s.ManagerIndexarr, which owns IndexerDefinition outright
// (pkg/k8s's field-manager table); the main resource and the status
// subresource are separate ownership sets, so the reconciler's status
// applies and these spec applies never release each other's fields. Every
// apply is the complete declaration: the label and spec.yaml.
//
// Files the bundle refuses (a schema failure, a duplicate id, an oversized
// file) are logged one per line with their bounded reason and skipped; a
// directory that cannot be read at all fails the loader, and with it the
// manager, because an operator who configured a bundle that is not there
// should find out at startup rather than from every Indexer that names one
// of its ids.
package bundle

// The loader reads a same-named IndexerDefinition before deciding, and
// creates or updates its own. Events are not written.
//
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexerdefinitions,verbs=get;list;watch;create;patch
