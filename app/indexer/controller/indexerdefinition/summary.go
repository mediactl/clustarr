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

package indexerdefinition

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// maxCategories is the CRD's +kubebuilder:validation:MaxItems on
// status.caps.categories. Exceeding it is an apiserver rejection of the whole
// apply, not a truncation, so the controller truncates deliberately (lowest
// ids first, after sorting) rather than having every status write fail. No
// bundled definition comes close: the largest maps a few dozen.
const maxCategories = 200

// maxReplaces is the CRD's +kubebuilder:validation:MaxItems on
// status.replaces, enforced here for the same reason as maxCategories. The
// upstream corpus's longest replaces list is a handful of ids.
const maxReplaces = 32

// cardigannModes maps Cardigann's own mode names onto the Torznab wire values
// that pkg/torznab implements and torznab.Caps.Supports compares against.
//
// This mapping is the point of the whole file. The two vocabularies differ for
// three of the five modes, the CRD's map key carries no enum marker, and
// nothing downstream would report a mismatch: a caps gate keyed on
// "tv-search" simply never matches, so the indexer is never queried and no
// error is ever logged. The schema's Modes object is additionalProperties:
// false over exactly these five keys, so anything else cannot survive
// cardigann.Validate; an unknown key is dropped rather than passed through,
// because passing it through is precisely the silent never-matches failure.
var cardigannModes = map[string]string{
	"search":       string(torznab.ModeSearch),      // "search"
	"tv-search":    string(torznab.ModeTVSearch),    // "tvsearch"
	"movie-search": string(torznab.ModeMovieSearch), // "movie"
	"music-search": string(torznab.ModeMusicSearch), // "music"
	"book-search":  string(torznab.ModeBookSearch),  // "book"
}

// cardigannPrivacy maps the schema's tracker-privacy enum onto the CRD's.
// They agree on two of three values and differ on the middle one:
// pkg/cardigann.DefinitionType spells it "semi-private", the CRD enum is
// "semiPrivate", and status.type carries that enum in the generated CRD -- so
// sending the Cardigann spelling is an apiserver rejection of the apply.
var cardigannPrivacy = map[cardigann.DefinitionType]indexv1alpha1.DefinitionType{
	"public":       indexv1alpha1.DefinitionTypePublic,
	"semi-private": indexv1alpha1.DefinitionTypeSemiPrivate,
	"private":      indexv1alpha1.DefinitionTypePrivate,
}

// summary is the complete set k8s.ManagerIndexarr owns on
// IndexerDefinition.status, minus the conditions and observedGeneration that
// every path computes fresh.
//
// It exists so that there is exactly ONE description of the owned set: every
// path seeds it from the live status with summaryFrom, mutates what it can
// recompute, and hands the whole thing to statusFor. A path that built its own
// apply configuration would release whatever it forgot -- server-side apply
// replaces a manager's ownership set on every apply -- and the early-return
// path is where that bites, because it is the transient one.
type summary struct {
	ID         string
	Replaces   []string
	Name       string
	Language   string
	Type       indexv1alpha1.DefinitionType
	Protocol   commonv1alpha1.Protocol
	Sha256     string
	Modes      map[string][]string
	Categories []int32
}

// summaryFrom seeds the owned set from the live status, so that a reconcile
// which cannot recompute the summary re-sends the values already on the
// object instead of releasing them.
func summaryFrom(st indexv1alpha1.IndexerDefinitionStatus) summary {
	return summary{
		ID:         st.ID,
		Replaces:   st.Replaces,
		Name:       st.Name,
		Language:   st.Language,
		Type:       st.Type,
		Protocol:   st.Protocol,
		Sha256:     st.Sha256,
		Modes:      st.Caps.Modes,
		Categories: st.Caps.Categories,
	}
}

// summarise projects a decoded definition onto the owned set. Pure: no I/O, no
// cluster, no network -- it is a decode of yaml, which the caller has already
// validated, plus the two vocabulary mappings above.
//
// protocol is unconditionally torrent. A Cardigann v11 definition describes a
// torrent tracker: the schema has no protocol key and no usenet notion, and
// the download block is built around magnet links and infohashes. Usenet
// indexers are Newznab and are configured through Indexer.spec.generic, which
// carries its own protocol field. If a later schema revision adds usenet, this
// is the line to change.
func summarise(def *cardigann.Definition, yaml string) summary {
	caps := def.Capabilities()
	return summary{
		ID:         def.ID,
		Replaces:   replacedIDs(def.ID, def.Replaces),
		Name:       def.Name,
		Language:   def.Language,
		Type:       cardigannPrivacy[def.Type],
		Protocol:   commonv1alpha1.ProtocolTorrent,
		Sha256:     digest(yaml),
		Modes:      torznabModes(caps.Modes),
		Categories: categoryIDs(caps.Categories),
	}
}

// replacedIDs is status.replaces: the definition's own replaces list, in its
// declared order, without blanks, repeats or its own id (an alias for itself
// would say nothing), capped at the CRD's MaxItems.
func replacedIDs(id string, replaces []string) []string {
	var out []string
	for _, old := range replaces {
		if old == "" || old == id || slices.Contains(out, old) {
			continue
		}
		out = append(out, old)
		if len(out) == maxReplaces {
			break
		}
	}
	return out
}

// digest is status.sha256: the hex digest of spec.yaml as last validated.
func digest(yaml string) string {
	sum := sha256.Sum256([]byte(yaml))
	return hex.EncodeToString(sum[:])
}

// torznabModes renames a definition's mode keys to Torznab's wire values.
func torznabModes(modes map[string][]string) map[string][]string {
	if len(modes) == 0 {
		return nil
	}
	out := make(map[string][]string, len(modes))
	for name, params := range modes {
		wire, ok := cardigannModes[name]
		if !ok {
			continue // unreachable after cardigann.Validate; see cardigannModes
		}
		out[wire] = params
	}
	return out
}

// categoryIDs converts resolved Newznab ids to the CRD's int32 list, sorted so
// that a reordering inside the definition does not churn status, and capped at
// the CRD's MaxItems.
func categoryIDs(cats []newznab.CategoryID) []int32 {
	if len(cats) == 0 {
		return nil
	}
	out := make([]int32, 0, len(cats))
	for _, id := range cats {
		out = append(out, int32(id))
	}
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) > maxCategories {
		out = out[:maxCategories]
	}
	return out
}

// statusFor builds the ONE apply configuration this package ever sends. Every
// field k8s.ManagerIndexarr owns is declared here, on every path, because
// server-side apply replaces a manager's ownership set rather than merging
// into it and a field omitted once is released.
//
// type and protocol are the two exceptions, and they are shape rather than
// outcome: both carry an enum in the generated CRD (public|semiPrivate|private
// and torrent|usenet), so an explicit "" is rejected outright. They are empty
// only before the first successful parse, never because a particular reconcile
// went badly.
func statusFor(generation int64, s summary, conditions []metav1.Condition) *indexac.IndexerDefinitionStatusApplyConfiguration {
	ac := indexac.IndexerDefinitionStatus().
		WithObservedGeneration(generation).
		WithConditions(k8s.ConditionACs(conditions)...).
		WithID(s.ID).
		WithReplaces(s.Replaces...).
		WithName(s.Name).
		WithLanguage(s.Language).
		WithSha256(s.Sha256).
		WithCaps(indexac.CapsSummary().
			WithModes(s.Modes).
			WithCategories(s.Categories...))
	if s.Type != "" {
		ac = ac.WithType(s.Type)
	}
	if s.Protocol != "" {
		ac = ac.WithProtocol(s.Protocol)
	}
	return ac
}
