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

package v1alpha1

import (
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Condition types and reasons reported on a BacklogSearch.
const (
	// BacklogSearchConditionReady is True while the BacklogSearch runs its
	// namespace's backlog searches.
	BacklogSearchConditionReady = "Ready"
	// BacklogSearchReasonActive: the cycles run.
	BacklogSearchReasonActive = "Active"
	// BacklogSearchReasonPaused: spec.enabled is false.
	BacklogSearchReasonPaused = "Paused"
	// BacklogSearchReasonOverlap: an older BacklogSearch in the namespace
	// runs its backlog searches; this one runs none.
	BacklogSearchReasonOverlap = "Overlap"
)

// The defaults and bounds of a BacklogSearch's spec, applied by its
// accessors: a typed client cannot send a value a CRD default would
// override (CLAUDE.md, "A typed Go client cannot send a value the CRD
// default overrides"), and the two queues default differently.
const (
	DefaultBacklogInterval                  = 15 * time.Minute
	MinBacklogInterval                      = time.Minute
	DefaultBacklogHourlyLimit         int32 = 20
	MaxBacklogHourlyLimit             int32 = 100
	DefaultBacklogMissingPerCycle     int32 = 5
	DefaultBacklogCutoffUnmetPerCycle int32 = 1
	DefaultBacklogMissingCooldown           = 7 * 24 * time.Hour
	DefaultBacklogCutoffUnmetCooldown       = 14 * 24 * time.Hour
	MinBacklogCooldown                      = time.Hour
	// MaxBacklogRecent is status.recent's MaxItems, and so hourlyLimit's
	// bound: after a leader change the last hour's searches are counted
	// from it.
	MaxBacklogRecent = 100
)

// BacklogQueueSpec is one queue of a BacklogSearch: the missing items, or
// the ones below their profile's cutoff.
type BacklogQueueSpec struct {
	// PerCycle is how many of the queue's searches a cycle starts at most:
	// one item, or one season (spec.seasonSearch). 0 turns the queue off.
	// Unset: 5 for missing, 1 for cutoffUnmet.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=50
	PerCycle *int32 `json:"perCycle,omitempty"`

	// Cooldown is how long after an item's last live search (its
	// status.lastLiveSearchAt) it may be searched live again by the
	// backlog. Unset: 168h for missing, 336h for cutoffUnmet; at least 1h.
	// +optional
	Cooldown *metav1.Duration `json:"cooldown,omitempty"`
}

// BacklogSearchSpec is how a namespace's backlog is searched.
type BacklogSearchSpec struct {
	// Enabled runs the cycles; false pauses them. Unset: true.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Interval is the time between cycles. Unset: 15m; at least 1m.
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`

	// HourlyLimit is how many backlog searches start in any hour at most,
	// across both queues. Every search asks each of the namespace's
	// indexers once, so it is read per indexer. Unset: 20.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	HourlyLimit *int32 `json:"hourlyLimit,omitempty"`

	// Missing is the queue of monitored items with no file.
	// +optional
	Missing BacklogQueueSpec `json:"missing,omitempty"`

	// CutoffUnmet is the queue of monitored items whose file is below its
	// profile's cutoff.
	// +optional
	CutoffUnmet BacklogQueueSpec `json:"cutoffUnmet,omitempty"`

	// Kinds limits the backlog to these kinds; empty is every searchable
	// kind.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=6
	// +kubebuilder:validation:items:Enum=movie;episode;album;book;audiobook;issue
	Kinds []commonv1.MediaKind `json:"kinds,omitempty"`

	// SeasonSearch searches two or more due episodes of one season as one
	// season query, through the Series' automatic season search, where a
	// lone due episode searches alone. Unset: true.
	// +optional
	SeasonSearch *bool `json:"seasonSearch,omitempty"`
}

// IsEnabled is spec.enabled, true when unset.
func (s BacklogSearchSpec) IsEnabled() bool { return ptr.Deref(s.Enabled, true) }

// IntervalOrDefault is spec.interval, DefaultBacklogInterval when unset,
// never under MinBacklogInterval.
func (s BacklogSearchSpec) IntervalOrDefault() time.Duration {
	d := DefaultBacklogInterval
	if s.Interval != nil {
		d = s.Interval.Duration
	}
	return max(d, MinBacklogInterval)
}

// HourlyLimitOrDefault is spec.hourlyLimit, DefaultBacklogHourlyLimit when
// unset, never over MaxBacklogHourlyLimit.
func (s BacklogSearchSpec) HourlyLimitOrDefault() int32 {
	return min(max(ptr.Deref(s.HourlyLimit, DefaultBacklogHourlyLimit), 0), MaxBacklogHourlyLimit)
}

// SeasonSearchOrDefault is spec.seasonSearch, true when unset.
func (s BacklogSearchSpec) SeasonSearchOrDefault() bool { return ptr.Deref(s.SeasonSearch, true) }

// MissingPerCycle is spec.missing.perCycle with its default.
func (s BacklogSearchSpec) MissingPerCycle() int32 {
	return max(ptr.Deref(s.Missing.PerCycle, DefaultBacklogMissingPerCycle), 0)
}

// CutoffUnmetPerCycle is spec.cutoffUnmet.perCycle with its default.
func (s BacklogSearchSpec) CutoffUnmetPerCycle() int32 {
	return max(ptr.Deref(s.CutoffUnmet.PerCycle, DefaultBacklogCutoffUnmetPerCycle), 0)
}

// MissingCooldown is spec.missing.cooldown with its default and floor.
func (s BacklogSearchSpec) MissingCooldown() time.Duration {
	return cooldown(s.Missing.Cooldown, DefaultBacklogMissingCooldown)
}

// CutoffUnmetCooldown is spec.cutoffUnmet.cooldown with its default and
// floor.
func (s BacklogSearchSpec) CutoffUnmetCooldown() time.Duration {
	return cooldown(s.CutoffUnmet.Cooldown, DefaultBacklogCutoffUnmetCooldown)
}

func cooldown(d *metav1.Duration, def time.Duration) time.Duration {
	if d == nil {
		return def
	}
	return max(d.Duration, MinBacklogCooldown)
}

// Searches reports whether the backlog searches kind: listed, or no kind
// listed.
func (s BacklogSearchSpec) Searches(kind commonv1.MediaKind) bool {
	return len(s.Kinds) == 0 || slices.Contains(s.Kinds, kind)
}

// BacklogQueueStatus is one queue as the last cycle found it.
type BacklogQueueStatus struct {
	// Candidates are the queue's items: monitored, wanted for the queue's
	// reason, searched before, nothing in flight.
	Candidates int32 `json:"candidates"`
	// Due are the candidates past the queue's cooldown.
	Due int32 `json:"due"`
	// OldestLiveSearchAt is the oldest last live search among the due
	// candidates that have one.
	// +optional
	OldestLiveSearchAt *metav1.Time `json:"oldestLiveSearchAt,omitempty"`
}

// BacklogSearchUnit is one backlog search a cycle started: an item, or a
// season of a series.
type BacklogSearchUnit struct {
	// Kind is the item's kind, series for a season.
	Kind commonv1.MediaKind `json:"kind"`
	// Name is the item's, or the series'.
	Name string `json:"name"`
	// Season is a season unit's season.
	// +optional
	Season *int32 `json:"season,omitempty"`
	// Items is how many items the unit searches for: 1, or a season's due
	// episodes.
	Items int32 `json:"items"`
	// Reason is the queue: missing or cutoffUnmet.
	// +kubebuilder:validation:Enum=missing;cutoffUnmet
	Reason string `json:"reason"`
	// At is when the cycle started it.
	At metav1.Time `json:"at"`
}

// BacklogSearchStatus describes the observed state of BacklogSearch.
type BacklogSearchStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// LastCycleAt is when the last cycle ran.
	// +optional
	LastCycleAt *metav1.Time `json:"lastCycleAt,omitempty"`

	// NextCycleAt is when the next cycle runs.
	// +optional
	NextCycleAt *metav1.Time `json:"nextCycleAt,omitempty"`

	// Missing is the missing queue as the last cycle found it.
	// +optional
	Missing *BacklogQueueStatus `json:"missing,omitempty"`

	// CutoffUnmet is the cutoff-unmet queue as the last cycle found it.
	// +optional
	CutoffUnmet *BacklogQueueStatus `json:"cutoffUnmet,omitempty"`

	// SearchesLastHour counts the backlog searches started in the hour
	// before the last cycle, its own included.
	// +optional
	SearchesLastHour int32 `json:"searchesLastHour,omitempty"`

	// Recent are the latest backlog searches, newest first.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=100
	Recent []BacklogSearchUnit `json:"recent,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=bls,categories=clustarr;catalog
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Missing",type=integer,JSONPath=`.status.missing.due`
// +kubebuilder:printcolumn:name="CutoffUnmet",type=integer,JSONPath=`.status.cutoffUnmet.due`
// +kubebuilder:printcolumn:name="LastHour",type=integer,JSONPath=`.status.searchesLastHour`
// +kubebuilder:printcolumn:name="LastCycle",type=date,JSONPath=`.status.lastCycleAt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BacklogSearch searches a namespace's backlog -- missing items and items
// below their cutoff -- against the indexers again, a few at a time: every
// interval it picks the items whose last live search is oldest and lets
// each make one live search, behind the namespace's live-search pace and
// the dispatch ledger. Without it an item is searched live once, and
// afterwards only against the release index its indexers' RSS feeds fill
// (docs/superpowers/specs/2026-11-10-backlog-search-design.md).
type BacklogSearch struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BacklogSearchSpec   `json:"spec,omitempty"`
	Status BacklogSearchStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// BacklogSearchList contains a list of BacklogSearch.
type BacklogSearchList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BacklogSearch `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BacklogSearch{}, &BacklogSearchList{})
}
