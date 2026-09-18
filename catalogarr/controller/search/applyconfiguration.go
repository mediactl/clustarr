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

package search

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// searchAPIVersion is the group/version Search lives in; an apply
// configuration must carry apiVersion and kind as literal strings on the
// wire, so they are taken from the package's own GroupVersion rather than
// hard-coded here.
var searchAPIVersion = catalogv1alpha1.GroupVersion.String()

// WorkerOutcomeName is the status.indexerOutcomes entry name the search worker
// reserves for a failure that is not any one indexer's fault -- an invalid
// QualityProfile, an unsupported media kind, a target that vanished mid-flight.
//
// It lives here rather than in the worker because both halves need it and the
// dependency only points one way: the worker imports this package for the
// apply configuration, so this package cannot import the worker. The value is
// deliberately not a valid DNS-1123 subdomain, so it can never collide with a
// real Indexer object's name in a listType=map keyed by name.
const WorkerOutcomeName = "catalogarr/search-worker"

// SearchApplyConfiguration is a hand-written stand-in for the generated apply
// configuration controller-tools cannot produce for Search (see the doc
// comment on catalogv1alpha1.Search: ReleaseDecision inlines
// common.ReleaseInfo and the generator panics rewriting the cross-package
// $refs). It carries only what this package's controller and worker ever
// patch: name/namespace/kind/apiVersion and status.
//
// Nested lists (IndexerOutcomes, Results, Grabbed) are plain catalogv1alpha1
// values rather than further apply-configuration wrappers, exactly as
// DownloadSpecApplyConfiguration embeds *commonv1alpha1.ReleaseInfo directly:
// every write here replaces the whole list, never one element of it, so a
// plain value slice is both correct and simpler than reproducing
// controller-tools' generator for three more types.
//
// Any later task that needs to patch Search.status should reuse this type
// rather than waiting for codegen to grow one.
type SearchApplyConfiguration struct {
	metav1ac.TypeMetaApplyConfiguration    `json:",inline"`
	*metav1ac.ObjectMetaApplyConfiguration `json:"metadata,omitempty"`

	Status *SearchStatusApplyConfiguration `json:"status,omitempty"`
}

// SearchStatusApplyConfiguration is the status half of
// SearchApplyConfiguration.
//
// The three list fields are pointers to slices, not slices, and for
// status.results that is load-bearing. Under server-side apply a field a
// manager omits is RELEASED, so "this list is now empty" and "I have nothing
// to say about this list" have to be two different things on the wire. A plain
// []T with `omitempty` cannot express the first: encoding/json omits on
// LENGTH, not on nil-ness, so an explicitly-emptied slice marshals away to
// nothing and silently degrades into the second. A *[]T separates them
// exactly -- a nil pointer omits the field, a pointer to an empty slice sends
// `[]`. Dropping `omitempty` instead would be worse: a builder that
// legitimately never touches results would then send `"results": null` and
// claim a field it does not own.
//
// # The distinction does NOT exist for the other two lists, and that matters
//
// status.results is an atomic list. status.indexerOutcomes and status.grabbed
// are `listType=map`, and server-side apply tracks an associative list per
// ENTRY, not as a whole: ownership is recorded as k:{"name":"idx"} under the
// field, and an empty list owns nothing. Applying `[]` and omitting the field
// therefore produce a byte-identical object and a byte-identical ownership
// record -- in both cases the entries this manager owned are removed. Verified
// against a real apiserver in ownership_envtest_test.go.
//
// So the pointer shape is kept on all three for uniformity, and because a
// listType marker could change, but it must not be mistaken for a guarantee on
// the two associative lists. The only thing that preserves an associative
// list's contents across a write is re-declaring the contents -- the
// read-modify-declare cycle Worker.writeFailure and Reconciler's
// newStatusUpdate both perform.
//
// The same caveat applies to the pointer's usefulness on results: declaring
// `results: []` removes an earlier run's results just as thoroughly as
// releasing the field did. What it buys is an honest ownership record and a
// doc comment that is true; what actually saves a user's results is the
// caller re-declaring them.
//
// Conditions keeps the plain-slice shape every generated apply configuration
// in this repo uses. It is `listType=map` too, so a pointer would buy it
// nothing, and every write path here sets at least one condition.
type SearchStatusApplyConfiguration struct {
	ObservedGeneration *int64                                  `json:"observedGeneration,omitempty"`
	Conditions         []*metav1ac.ConditionApplyConfiguration `json:"conditions,omitempty"`
	Phase              *catalogv1alpha1.SearchPhase            `json:"phase,omitempty"`
	StartedAt          *metav1.Time                            `json:"startedAt,omitempty"`
	FinishedAt         *metav1.Time                            `json:"finishedAt,omitempty"`
	IndexerOutcomes    *[]catalogv1alpha1.IndexerOutcome       `json:"indexerOutcomes,omitempty"`
	Results            *[]catalogv1alpha1.ReleaseDecision      `json:"results,omitempty"`
	Grabbed            *[]catalogv1alpha1.GrabResult           `json:"grabbed,omitempty"`
}

// Search builds an empty apply configuration for the named Search.
func Search(name, namespace string) *SearchApplyConfiguration {
	b := &SearchApplyConfiguration{}
	b.WithName(name)
	b.WithNamespace(namespace)
	b.WithKind("Search")
	b.WithAPIVersion(searchAPIVersion)
	return b
}

// SearchStatus builds an empty Search status apply configuration.
func SearchStatus() *SearchStatusApplyConfiguration { return &SearchStatusApplyConfiguration{} }

// IsApplyConfiguration marks this type as an apply configuration for
// runtime.ApplyConfiguration and therefore for k8s.ApplyConfiguration.
func (b *SearchApplyConfiguration) IsApplyConfiguration() {}

func (b *SearchApplyConfiguration) ensureObjectMeta() {
	if b.ObjectMetaApplyConfiguration == nil {
		b.ObjectMetaApplyConfiguration = &metav1ac.ObjectMetaApplyConfiguration{}
	}
}

// WithName sets metadata.name.
func (b *SearchApplyConfiguration) WithName(v string) *SearchApplyConfiguration {
	b.ensureObjectMeta()
	b.Name = &v
	return b
}

// WithNamespace sets metadata.namespace.
func (b *SearchApplyConfiguration) WithNamespace(v string) *SearchApplyConfiguration {
	b.ensureObjectMeta()
	b.Namespace = &v
	return b
}

// WithKind sets kind.
func (b *SearchApplyConfiguration) WithKind(v string) *SearchApplyConfiguration {
	b.Kind = &v
	return b
}

// WithAPIVersion sets apiVersion.
func (b *SearchApplyConfiguration) WithAPIVersion(v string) *SearchApplyConfiguration {
	b.APIVersion = &v
	return b
}

// WithStatus sets the status the apply declares.
func (b *SearchApplyConfiguration) WithStatus(v *SearchStatusApplyConfiguration) *SearchApplyConfiguration {
	b.Status = v
	return b
}

// GetName implements k8s.ApplyConfiguration.
func (b *SearchApplyConfiguration) GetName() *string {
	b.ensureObjectMeta()
	return b.Name
}

// GetNamespace implements k8s.ApplyConfiguration.
func (b *SearchApplyConfiguration) GetNamespace() *string {
	b.ensureObjectMeta()
	return b.Namespace
}

// GetKind implements k8s.ApplyConfiguration.
func (b *SearchApplyConfiguration) GetKind() *string { return b.Kind }

// GetAPIVersion implements k8s.ApplyConfiguration.
func (b *SearchApplyConfiguration) GetAPIVersion() *string {
	return b.APIVersion
}

// WithObservedGeneration sets status.observedGeneration.
func (b *SearchStatusApplyConfiguration) WithObservedGeneration(v int64) *SearchStatusApplyConfiguration {
	b.ObservedGeneration = &v
	return b
}

// WithConditions appends to status.conditions.
func (b *SearchStatusApplyConfiguration) WithConditions(v ...*metav1ac.ConditionApplyConfiguration) *SearchStatusApplyConfiguration {
	b.Conditions = append(b.Conditions, v...)
	return b
}

// WithPhase sets status.phase.
func (b *SearchStatusApplyConfiguration) WithPhase(v catalogv1alpha1.SearchPhase) *SearchStatusApplyConfiguration {
	b.Phase = &v
	return b
}

// WithStartedAt sets status.startedAt.
func (b *SearchStatusApplyConfiguration) WithStartedAt(v metav1.Time) *SearchStatusApplyConfiguration {
	b.StartedAt = &v
	return b
}

// WithFinishedAt sets status.finishedAt.
func (b *SearchStatusApplyConfiguration) WithFinishedAt(v metav1.Time) *SearchStatusApplyConfiguration {
	b.FinishedAt = &v
	return b
}

// WithIndexerOutcomes declares status.indexerOutcomes and appends to it.
//
// Calling it at all is the declaration, so WithIndexerOutcomes() with no
// arguments -- or with an empty slice spread into it -- sends `[]` and keeps
// ownership of the field, rather than omitting it and releasing whatever was
// there. That is the whole reason the field is a pointer; see the struct's
// doc comment.
func (b *SearchStatusApplyConfiguration) WithIndexerOutcomes(v ...catalogv1alpha1.IndexerOutcome) *SearchStatusApplyConfiguration {
	if b.IndexerOutcomes == nil {
		b.IndexerOutcomes = &[]catalogv1alpha1.IndexerOutcome{}
	}
	*b.IndexerOutcomes = append(*b.IndexerOutcomes, v...)
	return b
}

// WithResults declares status.results and appends to it. Calling it with no
// arguments declares the list empty; see WithIndexerOutcomes.
func (b *SearchStatusApplyConfiguration) WithResults(v ...catalogv1alpha1.ReleaseDecision) *SearchStatusApplyConfiguration {
	if b.Results == nil {
		b.Results = &[]catalogv1alpha1.ReleaseDecision{}
	}
	*b.Results = append(*b.Results, v...)
	return b
}

// WithGrabbed declares status.grabbed and appends to it. Calling it with no
// arguments declares the list empty; see WithIndexerOutcomes.
func (b *SearchStatusApplyConfiguration) WithGrabbed(v ...catalogv1alpha1.GrabResult) *SearchStatusApplyConfiguration {
	if b.Grabbed == nil {
		b.Grabbed = &[]catalogv1alpha1.GrabResult{}
	}
	*b.Grabbed = append(*b.Grabbed, v...)
	return b
}
