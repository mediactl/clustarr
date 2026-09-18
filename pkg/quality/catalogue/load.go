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

// Package catalogue is Clustarr's opinionated TRaSH custom-format catalogue.
//
// hack/gen-catalogue and catalogue_gen.go do not exist yet: spec's design
// (docs/superpowers/specs/2026-09-18-clustarr-design.md §7, §9) describes a
// generator that produces this package's data mechanically from a vendored
// copy of TRaSH-Guides' JSON corpus. That generator is out of scope for
// Task B2 (path ownership pkg/quality/ only) and is carried to Phase C as a
// nice-to-decoding-path, not a blocker: this package's data/ is hand
// authored, but every embedded Format's trash_id and regex is proven
// byte-identical to the vendored corpus under testdata/trash/ by
// parity_test.go's TestEveryEmbeddedFormatMatchesItsCorpusSource, so
// generating catalogue_gen.go from the same corpus mechanically, later, is a
// pure refactor. Until hack/gen-catalogue lands, this file go:embeds the
// curated subset under data/: every custom format the 13 built-in profiles
// actually reference, transcribed with real trash_ids, scores and regexes
// from the vendored corpus (testdata/trash/docs/json/{radarr,sonarr}/cf) and
// cross-checked against docs/research/quality.md. When the generator lands,
// data/ is deleted and this file's embed directives point at
// catalogue_gen.go's output instead.
package catalogue

import (
	"embed"
	"encoding/json"
	"fmt"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
)

//go:embed data/formats/*.json
var formatFS embed.FS

// FormatFS exposes the embedded format family files for tests and for
// LoadedCatalogue.
//
// profileFS/ProfileFS (the equivalent accessor for data/profiles/*.json)
// are added once the first profile seed file exists (Step 35 below) --
// go:embed requires at least one matching file at compile time, so
// declaring an empty directory's directive here would break the build for
// every step in between.
func FormatFS() embed.FS { return formatFS }

// formatJSON mirrors Format's shape for decoding; Conditions[].Pattern is a
// string here and compiled into Condition.Pattern by DecodeFormats.
type formatJSON struct {
	Slug       string            `json:"slug"`
	Name       string            `json:"name"`
	TrashIDs   map[string]string `json:"trashIds"`
	Scores     map[string]int    `json:"scores"`
	Group      string            `json:"group"`
	Conditions []conditionJSON   `json:"conditions"`
}

type conditionJSON struct {
	Kind           string `json:"kind"`
	Name           string `json:"name"`
	Negate         bool   `json:"negate"`
	Required       bool   `json:"required"`
	Pattern        string `json:"pattern"`        // CondReleaseTitle, CondReleaseGroup
	Value          string `json:"value"`          // CondSource, CondModifier, CondLanguage, CondReleaseType (string enum value)
	Resolution     int32  `json:"resolution"`     // CondResolution
	ExceptLanguage bool   `json:"exceptLanguage"` // CondLanguage
	Flag           string `json:"flag"`           // CondIndexerFlag
}

// DecodeFormats parses one data/formats/*.json document (a JSON array of
// formats) and compiles every ReleaseTitle/ReleaseGroup pattern with
// compileTRaSH. A pattern that fails to compile fails the whole file --
// there is no partial catalogue.
func DecodeFormats(doc []byte) ([]*Format, error) {
	var raw []formatJSON
	if err := json.Unmarshal(doc, &raw); err != nil {
		return nil, fmt.Errorf("decode formats: %w", err)
	}
	formats := make([]*Format, 0, len(raw))
	for _, rf := range raw {
		f := &Format{Slug: rf.Slug, Name: rf.Name, TrashIDs: rf.TrashIDs, Scores: rf.Scores, Group: rf.Group}
		for _, rc := range rf.Conditions {
			cond := Condition{
				Kind: CondKind(rc.Kind), Name: rc.Name, Negate: rc.Negate, Required: rc.Required,
				Resolution: rc.Resolution, ExceptLanguage: rc.ExceptLanguage, Flag: rc.Flag,
			}
			switch cond.Kind {
			case CondReleaseTitle, CondReleaseGroup:
				re, err := compileTRaSH(rc.Pattern)
				if err != nil {
					return nil, fmt.Errorf("format %s: condition %s: compile pattern: %w", rf.Slug, rc.Name, err)
				}
				cond.Pattern = re
			case CondSource:
				cond.Source = common.Source(rc.Value)
			case CondModifier:
				cond.Modifier = common.Modifier(rc.Value)
			case CondLanguage:
				cond.Language = rc.Value
			case CondReleaseType:
				cond.ReleaseType = common.ReleaseType(rc.Value)
			}
			f.Conditions = append(f.Conditions, cond)
		}
		formats = append(formats, f)
	}
	return formats, nil
}
