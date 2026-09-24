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

// Command gen-catalogue regenerates pkg/quality/catalogue/data/formats/*.json
// from the vendored TRaSH-Guides corpus (test/data/trash/docs/json/{radarr,sonarr}/cf),
// closing the Phase B library carry described in
// docs/superpowers/specs/2026-09-18-clustarr-design.md §7/§9: the embedded
// catalogue is meant to be *generated*, not hand-typed.
//
// What is generated and what is curated data are deliberately split:
//
//   - The *selection* -- which of the corpus's ~480 custom formats Clustarr
//     embeds, which output family file each lands in, what order its
//     conditions appear in, and what catalogv1alpha1 FormatGroup it belongs
//     to -- is curation, not something mechanically derivable from the
//     corpus (the corpus has no notion of "Clustarr's curated subset"). That
//     selection is recorded once, in manifest/*.json (extracted from Phase
//     B's original hand-authored data/ when this generator was written; see
//     the task report), and is this generator's input alongside the corpus
//     itself.
//   - Everything error-prone about hand transcription -- the exact regex
//     text, the numeric-to-enum translations (Source/Modifier/ReleaseType
//     are encoded differently per app; Language by Radarr's numeric id),
//     Required/Negate, the format's display Name and its scores -- is
//     mechanically re-derived from the corpus every run. The manifest never
//     supplies a value, only a (Kind, Name) key to look one up by.
//
// Usage:
//
//	go run ./hack/gen-catalogue [-corpus test/data/trash/docs/json] [-manifest hack/gen-catalogue/manifest] [-out pkg/quality/catalogue/data/formats]
package main

// manifestApp names one app whose TrashIDs entry a format carries, in the
// order manifest/*.json lists it -- which is also the preference order for
// sourcing a condition's value when more than one app is listed (see
// resolveCondition): the first app present is authoritative.
type manifestApp struct {
	App     string `json:"app"`
	TrashID string `json:"trashId"`
}

// manifestCondition is a lookup key, not a value: (Kind, Name) identifies
// exactly one corpus specification inside the format's own corpus file(s),
// and every field the loader's conditionJSON needs beyond that is resolved
// from the corpus, never carried in the manifest.
type manifestCondition struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// manifestScore is one (score-set name, value) pair. Scores turned out,
// while building this generator, not to be a pure function of the corpus's
// own trash_scores for the format's own TrashIDs -- see the task report's
// "scores" finding: Phase B sometimes dropped the corpus's plain "default"
// entry for an anime-only format (it would double-count against the
// non-anime version of the same format), and at least once sourced a score
// from a *different* trash_id's corpus copy than the one its conditions
// come from (streaming.json's "amzn" takes its score from Sonarr's "amzn"
// CF despite its conditions coming from Radarr's differently-scored "AMZN"
// CF). That is curation, exactly like Slug/Group/condition selection, so
// scores are manifest data too, not mechanically regenerated every run.
type manifestScore struct {
	Key   string `json:"key"`
	Value int    `json:"value"`
}

// manifestFormat is one curated format: which corpus record(s) it draws
// from, where it belongs in the catalogue (Group), what its scores are, and
// which of the corpus's own specifications it embeds, in order.
type manifestFormat struct {
	Slug       string              `json:"slug"`
	Group      string              `json:"group,omitempty"`
	Apps       []manifestApp       `json:"apps"`
	Scores     []manifestScore     `json:"scores"`
	Conditions []manifestCondition `json:"conditions"`
}

// corpusSpecification is one *arr CustomFormatSpecification as the vendored
// corpus JSON shapes it -- the same shape parity_test.go decodes.
type corpusSpecification struct {
	Name           string `json:"name"`
	Implementation string `json:"implementation"`
	Negate         bool   `json:"negate"`
	Required       bool   `json:"required"`
	Fields         struct {
		Value          any  `json:"value"` // string (regex) or number (enum/id), kind-dependent
		ExceptLanguage bool `json:"exceptLanguage"`
	} `json:"fields"`
}

// corpusFormat is one corpus/docs/json/<app>/cf/*.json document.
type corpusFormat struct {
	TrashID        string                `json:"trash_id"`
	Name           string                `json:"name"`
	TrashScores    map[string]float64    `json:"trash_scores"`
	Specifications []corpusSpecification `json:"specifications"`
}

// resolvedCondition is a manifestCondition after its value has been looked
// up in the corpus and translated into the same vocabulary
// pkg/quality/catalogue/load.go's conditionJSON decodes -- ready to render.
type resolvedCondition struct {
	Kind           string
	Name           string
	Negate         bool
	Required       bool
	HasPattern     bool
	Pattern        string
	HasValue       bool
	Value          string
	HasResolution  bool
	Resolution     int32
	ExceptLanguage bool
	HasFlag        bool
	Flag           string
}

// scoreEntry is one (score-set name, value) pair, kept as a slice rather
// than a map because emission order is significant and not alphabetical
// (see assembleScores).
type scoreEntry struct {
	Key   string
	Value int
}

// resolvedFormat is a manifestFormat with every corpus-derived field filled
// in, ready to render into the loader's JSON shape.
type resolvedFormat struct {
	Slug       string
	Name       string
	TrashIDs   []manifestApp
	Scores     []scoreEntry
	Group      string
	Conditions []resolvedCondition
}
