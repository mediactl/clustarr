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
//
// Every tier the 13 built-in profiles' real upstream formatItems reference
// is embedded, not just Tier 01 of each release-group family: the original
// plan for this task truncated HD/UHD Bluray, Remux and WEB to Tier 01 and
// called Tier 02/03 a stated gap, since the note the plan was drafted from
// gave full group lists only for Tier 01. With the corpus vendored, that
// truncation was voided -- data/formats/tiers_extra.json (Tier 02/03) and
// data/formats/anime_extra.json (Anime BD Tier 02-08, Anime Web Tier 02-06,
// and the remaining anime-only formats) are a mechanical read of the real
// corpus files, not a guess, and parity_test.go proves it.
package catalogue

import (
	"embed"
	"encoding/json"
	"fmt"
	"sync"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
)

//go:embed data/formats/*.json
var formatFS embed.FS

//go:embed data/profiles/*.json
var profileFS embed.FS

// FormatFS exposes the embedded format family files for tests and for
// LoadedCatalogue.
func FormatFS() embed.FS { return formatFS }

// ProfileFS exposes the embedded profile seeds for pkg/quality to decode
// (pkg/quality/catalogue cannot decode them itself -- resolving a profile
// needs quality.Lookup, and quality already imports catalogue).
func ProfileFS() embed.FS { return profileFS }

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

var (
	loadedOnce sync.Once
	loaded     *Catalogue
	loadErr    error
)

// LoadedCatalogue returns the process-wide Catalogue built once from every
// data/formats/*.json file at first use. A malformed embedded file is a
// program bug baked in at compile time (the data is go:embed-ed, not read at
// runtime), so this panics rather than returning an error -- there is no
// reasonable recovery for a service that cannot load its own quality
// catalogue, and every embedded file already has its own decode test
// (load_test.go) that would have caught this before it ever reached
// LoadedCatalogue.
func LoadedCatalogue() *Catalogue {
	loadedOnce.Do(func() {
		entries, err := formatFS.ReadDir("data/formats")
		if err != nil {
			loadErr = err
			return
		}
		formats := map[string]*Format{}
		for _, e := range entries {
			doc, err := formatFS.ReadFile("data/formats/" + e.Name())
			if err != nil {
				loadErr = fmt.Errorf("%s: %w", e.Name(), err)
				return
			}
			fs, err := DecodeFormats(doc)
			if err != nil {
				loadErr = fmt.Errorf("%s: %w", e.Name(), err)
				return
			}
			for _, f := range fs {
				formats[f.Slug] = f
			}
		}
		loaded = &Catalogue{Version: "bootstrap-2026-09-18", Formats: formats}
	})
	if loadErr != nil {
		panic(fmt.Sprintf("quality/catalogue: LoadedCatalogue: %v", loadErr))
	}
	return loaded
}
