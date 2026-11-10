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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A PlexCollection is a Plex collection declared as a resource
// (docs/superpowers/specs/2026-11-10-plex-collections-design.md): a smart
// one, whose filter Plex evaluates, or a normal one, whose members it
// names, with the presentation it states. cluster-plex reconciles it into
// Plex and is the only writer of its status and of its finalizer (the
// design's option A, owner 2026-11-10); its spec is the UI's, kubectl's or
// Terraform's. Every optional field left out keeps what Plex has.

const (
	// PlexCollectionConditionSynced is True once Plex holds everything the
	// spec states.
	PlexCollectionConditionSynced = "Synced"

	// PlexCollectionFinalizer is cluster-plex's: it deletes the Plex
	// collection before the resource goes.
	PlexCollectionFinalizer = "clusterplex.io/plex-collection"

	// PlexCollectionStatusManager is the field manager cluster-plex writes
	// status and the finalizer under.
	PlexCollectionStatusManager = "cluster-plex"
)

// The Synced condition's reasons. Synced, Created, Adopted and
// MembersPending read True; the rest False.
const (
	PlexCollectionReasonSynced           = "Synced"
	PlexCollectionReasonCreated          = "Created"
	PlexCollectionReasonAdopted          = "Adopted"
	PlexCollectionReasonMembersPending   = "MembersPending"
	PlexCollectionReasonWaiting          = "Waiting"
	PlexCollectionReasonInvalidRule      = "InvalidRule"
	PlexCollectionReasonUnknownValue     = "UnknownValue"
	PlexCollectionReasonLibraryNotFound  = "LibraryNotFound"
	PlexCollectionReasonNotSmart         = "NotSmart"
	PlexCollectionReasonNotAdoptable     = "NotAdoptable"
	PlexCollectionReasonConflict         = "Conflict"
	PlexCollectionReasonImageUnavailable = "ImageUnavailable"
	PlexCollectionReasonPlexUnavailable  = "PlexUnavailable"
)

// PlexItemType is what a collection holds, as Plex types it.
// +kubebuilder:validation:Enum=movie;show;season;episode
type PlexItemType string

const (
	PlexItemMovie   PlexItemType = "movie"
	PlexItemShow    PlexItemType = "show"
	PlexItemSeason  PlexItemType = "season"
	PlexItemEpisode PlexItemType = "episode"
)

// PlexMatch joins a level's rules: all of them, or any.
// +kubebuilder:validation:Enum=all;any
type PlexMatch string

const (
	PlexMatchAll PlexMatch = "all"
	PlexMatchAny PlexMatch = "any"
)

// PlexOperator is a filter condition's operator, in words; cluster-plex
// turns it into Plex's symbol for the field's type
// (pkg/plexfilter.Operators says which a type takes).
// +kubebuilder:validation:Enum=is;isNot;contains;doesNotContain;beginsWith;endsWith;greaterThan;lessThan;before;after;isTrue;isFalse
type PlexOperator string

const (
	PlexOpIs             PlexOperator = "is"
	PlexOpIsNot          PlexOperator = "isNot"
	PlexOpContains       PlexOperator = "contains"
	PlexOpDoesNotContain PlexOperator = "doesNotContain"
	PlexOpBeginsWith     PlexOperator = "beginsWith"
	PlexOpEndsWith       PlexOperator = "endsWith"
	PlexOpGreaterThan    PlexOperator = "greaterThan"
	PlexOpLessThan       PlexOperator = "lessThan"
	PlexOpBefore         PlexOperator = "before"
	PlexOpAfter          PlexOperator = "after"
	PlexOpIsTrue         PlexOperator = "isTrue"
	PlexOpIsFalse        PlexOperator = "isFalse"
)

// PlexCollectionMode is Plex's collection mode: how the collection and
// its items show in the library.
// +kubebuilder:validation:Enum=default;hideCollection;hideItems;showCollectionAndItems
type PlexCollectionMode string

const (
	PlexModeDefault                PlexCollectionMode = "default"
	PlexModeHideCollection         PlexCollectionMode = "hideCollection"
	PlexModeHideItems              PlexCollectionMode = "hideItems"
	PlexModeShowCollectionAndItems PlexCollectionMode = "showCollectionAndItems"
)

// PlexCollectionOrder is a normal collection's item order in Plex.
// +kubebuilder:validation:Enum=releaseDate;alphabetical
type PlexCollectionOrder string

const (
	PlexOrderReleaseDate  PlexCollectionOrder = "releaseDate"
	PlexOrderAlphabetical PlexCollectionOrder = "alphabetical"
)

// PlexCondition is one row of a filter: a field, an operator and a value.
type PlexCondition struct {
	// Field is Plex's filter key, as a library's Meta lists it: "title",
	// "genre", "show.network", "episode.audioLanguage".
	// +kubebuilder:validation:Pattern=`^[a-zA-Z]+(\.[a-zA-Z]+)?$`
	Field string `json:"field"`

	// Operator compares the field with the value.
	Operator PlexOperator `json:"operator"`

	// Value is written as a person reads it: a tag's name ("Anime"), an
	// ISO 639-1 language ("en"), a date or Plex's relative form ("-6w").
	// isTrue and isFalse take none.
	// +optional
	Value string `json:"value,omitempty"`
}

// PlexFilterRule is a top-level entry of a filter: a condition, or a group
// of conditions with a match of its own. One level of groups.
// +kubebuilder:validation:XValidation:rule="has(self.field) != has(self.match)",message="a rule is a condition (field, operator) or a group (match, rules), not both"
// +kubebuilder:validation:XValidation:rule="!has(self.field) || has(self.operator)",message="a condition needs an operator"
// +kubebuilder:validation:XValidation:rule="!has(self.match) || has(self.rules)",message="a group needs rules"
type PlexFilterRule struct {
	// Field, Operator and Value make this rule a condition (PlexCondition).
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-zA-Z]+(\.[a-zA-Z]+)?$`
	Field string `json:"field,omitempty"`
	// +optional
	Operator PlexOperator `json:"operator,omitempty"`
	// +optional
	Value string `json:"value,omitempty"`

	// Match and Rules make this rule a group.
	// +optional
	Match PlexMatch `json:"match,omitempty"`
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	Rules []PlexCondition `json:"rules,omitempty"`
}

// PlexFilter is a smart collection's filter.
type PlexFilter struct {
	// Match joins the top-level rules.
	// +kubebuilder:default=all
	// +optional
	Match PlexMatch `json:"match,omitempty"`

	// Rules are the filter's conditions and groups, in order.
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	Rules []PlexFilterRule `json:"rules"`
}

// PlexSort is a smart collection's sort: Plex's sort key
// ("titleSort", "addedAt", "rating", "random").
type PlexSort struct {
	// +kubebuilder:validation:MinLength=1
	Field string `json:"field"`
	// +optional
	Descending bool `json:"descending,omitempty"`
}

// PlexMember is one item of a normal collection: a clustarr Movie or
// Series in the collection's namespace.
type PlexMember struct {
	// +kubebuilder:validation:Enum=Movie;Series
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// PlexMembers are a normal collection's members. An object, so that a
// selector can join Items later (the design's computed collections).
type PlexMembers struct {
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=1000
	Items []PlexMember `json:"items"`
}

// PlexImage is an image cluster-plex fetches and uploads to Plex: https
// on a public host, or anything under clustarr UI's own /art/.
type PlexImage struct {
	// +kubebuilder:validation:Pattern=`^https?://`
	URL string `json:"url"`
}

// PlexCollectionSettings are the collection's own Plex settings.
type PlexCollectionSettings struct {
	// Mode is how the collection and its items show in the library.
	// +optional
	Mode PlexCollectionMode `json:"mode,omitempty"`
	// Order is a normal collection's item order.
	// +optional
	Order PlexCollectionOrder `json:"order,omitempty"`
}

// PlexVisibility is where Plex promotes the collection as a hub of its
// own (Plex Web's "Visible on"). All three are set together.
type PlexVisibility struct {
	LibraryRecommended bool `json:"libraryRecommended"`
	Home               bool `json:"home"`
	FriendsHome        bool `json:"friendsHome"`
}

// PlexCollectionSpec is a Plex collection.
// +kubebuilder:validation:XValidation:rule="has(self.filter) != has(self.members)",message="a collection is smart (filter) or normal (members), not both"
// +kubebuilder:validation:XValidation:rule="has(self.filter) == has(oldSelf.filter)",message="a collection cannot change between smart and normal"
// +kubebuilder:validation:XValidation:rule="!has(self.members) || self.itemType in ['movie', 'show']",message="a normal collection holds movies or shows"
// +kubebuilder:validation:XValidation:rule="(!has(self.sort) && !has(self.limit)) || has(self.filter)",message="sort and limit belong to a smart collection"
// +kubebuilder:validation:XValidation:rule="!has(self.settings) || !has(self.settings.order) || has(self.members)",message="settings.order belongs to a normal collection; a smart one is ordered by its sort"
// +kubebuilder:validation:XValidation:rule="!has(self.members) || self.members.items.all(m, m.kind == (self.itemType == 'movie' ? 'Movie' : 'Series'))",message="a movie collection's members are Movies, a show collection's Series"
type PlexCollectionSpec struct {
	// Library is the Plex library section, by title.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="library is immutable"
	Library string `json:"library"`

	// Title is the collection's title in Plex.
	// +kubebuilder:validation:MinLength=1
	Title string `json:"title"`

	// ItemType is what the collection holds.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="itemType is immutable"
	ItemType PlexItemType `json:"itemType"`

	// Filter makes the collection smart.
	// +optional
	Filter *PlexFilter `json:"filter,omitempty"`

	// Sort is a smart collection's sort; absent keeps Plex's.
	// +optional
	Sort *PlexSort `json:"sort,omitempty"`

	// Limit is a smart collection's LIMIT TO; absent keeps Plex's.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Limit *int32 `json:"limit,omitempty"`

	// Members make the collection normal.
	// +optional
	Members *PlexMembers `json:"members,omitempty"`

	// Summary is the collection's summary; absent keeps Plex's.
	// +optional
	Summary *string `json:"summary,omitempty"`

	// SortTitle is the collection's sort title; absent keeps Plex's.
	// +optional
	SortTitle *string `json:"sortTitle,omitempty"`

	// Poster and Background are the collection's images; absent keeps
	// Plex's.
	// +optional
	Poster *PlexImage `json:"poster,omitempty"`
	// +optional
	Background *PlexImage `json:"background,omitempty"`

	// Settings are the collection's mode and order; absent keeps Plex's.
	// +optional
	Settings *PlexCollectionSettings `json:"settings,omitempty"`

	// Visibility is where Plex promotes the collection; absent keeps
	// Plex's.
	// +optional
	Visibility *PlexVisibility `json:"visibility,omitempty"`
}

// PlexImageStatus is the image cluster-plex last uploaded.
type PlexImageStatus struct {
	URL  string `json:"url"`
	SHA1 string `json:"sha1"`
	// +optional
	FetchedAt *metav1.Time `json:"fetchedAt,omitempty"`
}

// PlexCollectionStatus is cluster-plex's, alone.
type PlexCollectionStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// RatingKey is the Plex collection this resource holds.
	// +optional
	RatingKey string `json:"ratingKey,omitempty"`

	// Adopted is true when the collection existed before the resource.
	// +optional
	Adopted bool `json:"adopted,omitempty"`

	// Content is a smart collection's filter as Plex holds it after the
	// last sync.
	// +optional
	Content string `json:"content,omitempty"`

	// ItemCount is Plex's childCount after the last sync.
	// +optional
	ItemCount int32 `json:"itemCount,omitempty"`

	// MissingMembers counts a normal collection's members Plex does not
	// hold yet, and Missing names the first of them.
	// +optional
	MissingMembers int32 `json:"missingMembers,omitempty"`
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=20
	Missing []PlexMember `json:"missing,omitempty"`

	// Poster and Background are the images last uploaded.
	// +optional
	Poster *PlexImageStatus `json:"poster,omitempty"`
	// +optional
	Background *PlexImageStatus `json:"background,omitempty"`

	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=pcol,categories=clustarr;catalog
// +kubebuilder:printcolumn:name="Library",type=string,JSONPath=`.spec.library`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.spec.title`
// +kubebuilder:printcolumn:name="Items",type=integer,JSONPath=`.status.itemCount`
// +kubebuilder:printcolumn:name="Synced",type=string,JSONPath=`.status.conditions[?(@.type=="Synced")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PlexCollection is a Plex collection, smart or normal, declared as a
// resource; cluster-plex reconciles it into Plex.
type PlexCollection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PlexCollectionSpec   `json:"spec,omitempty"`
	Status PlexCollectionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// PlexCollectionList contains a list of PlexCollection.
type PlexCollectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PlexCollection `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PlexCollection{}, &PlexCollectionList{})
}
