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

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// condImplementation maps a catalogue.CondKind string (as manifest/*.json
// spells it) to the *arr specification "implementation" name the corpus
// uses -- the same mapping parity_test.go's condImplementation table
// carries, kept in lockstep with pkg/quality/catalogue.CondKind's own
// constants (see catalogue.go).
var condImplementation = map[string]string{
	string(catalogue.CondReleaseTitle): "ReleaseTitleSpecification",
	string(catalogue.CondReleaseGroup): "ReleaseGroupSpecification",
	string(catalogue.CondSource):       "SourceSpecification",
	string(catalogue.CondResolution):   "ResolutionSpecification",
	string(catalogue.CondModifier):     "QualityModifierSpecification",
	string(catalogue.CondLanguage):     "LanguageSpecification",
	string(catalogue.CondIndexerFlag):  "IndexerFlagSpecification",
	string(catalogue.CondReleaseType):  "ReleaseTypeSpecification",
	string(catalogue.CondEdition):      "EditionSpecification",
	string(catalogue.CondSize):         "SizeSpecification",
	string(catalogue.CondYear):         "YearSpecification",
}

// Per-app numeric-value translation tables, transcribed from
// pkg/quality/catalogue/parity_test.go's own radarrSourceByID /
// sonarrSourceByID / radarrModifierByID / releaseTypeByID -- that test is
// this generator's independent check (it re-derives the same values a
// second way, from the embedded output rather than the corpus input), so
// the two tables must agree by construction, not by copy-paste luck: a
// divergence here would show up as parity_test.go failing on generated
// output, not as a silent wrong value.
var (
	radarrSourceByID   = map[float64]string{5: "dvd", 7: "webdl", 8: "webrip", 9: "bluray"}
	sonarrSourceByID   = map[float64]string{3: "webdl", 4: "webrip", 5: "dvd", 6: "bluray"}
	radarrModifierByID = map[float64]string{5: "remux"}
	releaseTypeByID    = map[float64]string{3: "seasonPack"}
)

// numberField reads fields.value as a float64 regardless of whether the
// corpus JSON spelled it as an integer or a float literal -- encoding/json
// decodes every JSON number into a Go float64 when the destination is
// `any`, per encoding/json's documented default.
func numberField(v any, context string) (float64, error) {
	f, ok := v.(float64)
	if !ok {
		return 0, fmt.Errorf("%s: fields.value is not a number (got %T)", context, v)
	}
	return f, nil
}

func stringField(v any, context string) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s: fields.value is not a string (got %T)", context, v)
	}
	return s, nil
}

// resolveConditionFromSpec fills in the value-bearing fields of a
// resolvedCondition from one corpus specification, translating per Kind
// exactly as pkg/quality/catalogue/parity_test.go's checkConditionValue
// does for the reverse (embedded -> corpus) direction.
func resolveConditionFromSpec(kind, app string, spec corpusSpecification) (resolvedCondition, error) {
	rc := resolvedCondition{Kind: kind, Name: spec.Name, Negate: spec.Negate, Required: spec.Required}
	ctx := fmt.Sprintf("%s/%s (%s)", kind, spec.Name, app)
	switch catalogue.CondKind(kind) {
	case catalogue.CondReleaseTitle, catalogue.CondReleaseGroup:
		s, err := stringField(spec.Fields.Value, ctx)
		if err != nil {
			return rc, err
		}
		rc.HasPattern, rc.Pattern = true, s
	case catalogue.CondSource:
		n, err := numberField(spec.Fields.Value, ctx)
		if err != nil {
			return rc, err
		}
		table := radarrSourceByID
		if app == "sonarr" {
			table = sonarrSourceByID
		}
		v, ok := table[n]
		if !ok {
			return rc, fmt.Errorf("%s: no %s Source table entry for corpus value %v", ctx, app, n)
		}
		rc.HasValue, rc.Value = true, v
	case catalogue.CondModifier:
		n, err := numberField(spec.Fields.Value, ctx)
		if err != nil {
			return rc, err
		}
		if app != "radarr" {
			return rc, fmt.Errorf("%s: Sonarr has no QualityModifierSpecification usage in the corpus", ctx)
		}
		v, ok := radarrModifierByID[n]
		if !ok {
			return rc, fmt.Errorf("%s: no Modifier table entry for corpus value %v", ctx, n)
		}
		rc.HasValue, rc.Value = true, v
	case catalogue.CondResolution:
		n, err := numberField(spec.Fields.Value, ctx)
		if err != nil {
			return rc, err
		}
		rc.HasResolution, rc.Resolution = true, int32(n)
	case catalogue.CondLanguage:
		n, err := numberField(spec.Fields.Value, ctx)
		if err != nil {
			return rc, err
		}
		v, ok := catalogue.LanguageByID(int32(n))
		if !ok {
			return rc, fmt.Errorf("%s: no languageByID entry for corpus language id %v", ctx, n)
		}
		rc.HasValue, rc.Value = true, v
		rc.ExceptLanguage = spec.Fields.ExceptLanguage
	case catalogue.CondReleaseType:
		n, err := numberField(spec.Fields.Value, ctx)
		if err != nil {
			return rc, err
		}
		v, ok := releaseTypeByID[n]
		if !ok {
			return rc, fmt.Errorf("%s: no ReleaseType table entry for corpus value %v", ctx, n)
		}
		rc.HasValue, rc.Value = true, v
	case catalogue.CondIndexerFlag:
		return rc, fmt.Errorf("%s: IndexerFlag translation is not implemented -- no embedded format uses it yet (see parity_test.go's own checkConditionValue)", ctx)
	case catalogue.CondEdition, catalogue.CondSize, catalogue.CondYear:
		// accepted no-ops: nothing to translate.
	default:
		return rc, fmt.Errorf("%s: unhandled condition kind %q", ctx, kind)
	}
	return rc, nil
}

// resolveCondition resolves one manifest condition against every app the
// format lists, in order, using the first app as authoritative and
// requiring every other app's copy to translate to the *same* resolved
// value -- a stronger check than parity_test.go performs (which only
// checks the single embedded value against each app independently), and
// one that would have caught a merged format silently picking up one app's
// divergent copy.
func resolveCondition(idx corpusIndexes, apps []manifestApp, mc manifestCondition) (resolvedCondition, error) {
	impl, ok := condImplementation[mc.Kind]
	if !ok {
		return resolvedCondition{}, fmt.Errorf("condition %q: no corpus implementation mapping for kind %q", mc.Name, mc.Kind)
	}
	var out resolvedCondition
	for i, a := range apps {
		spec, _, err := idx.findSpecification(a.App, a.TrashID, impl, mc.Name)
		if err != nil {
			return resolvedCondition{}, err
		}
		rc, err := resolveConditionFromSpec(mc.Kind, a.App, spec)
		if err != nil {
			return resolvedCondition{}, err
		}
		if i == 0 {
			out = rc
			continue
		}
		if rc != out {
			return resolvedCondition{}, fmt.Errorf("condition %q: %s's corpus copy resolves to %+v, %s's resolves to %+v -- apps disagree, pick one explicitly instead of silently trusting the first", mc.Name, apps[0].App, out, a.App, rc)
		}
	}
	return out, nil
}

// resolveFormat resolves one manifest format against the corpus: its Name
// (from the first app's corpus record) and every Condition
// (resolveCondition), in the manifest's own condition order. Scores are
// carried through from the manifest verbatim, in the manifest's own order
// -- see manifestScore's doc comment for why they are curated data, not
// corpus-derived.
func resolveFormat(idx corpusIndexes, mf manifestFormat) (resolvedFormat, error) {
	if len(mf.Apps) == 0 {
		return resolvedFormat{}, fmt.Errorf("format %q: no apps listed", mf.Slug)
	}
	first := mf.Apps[0]
	firstCF, ok := idx[first.App][first.TrashID]
	if !ok {
		return resolvedFormat{}, fmt.Errorf("format %q: %s has no corpus entry for trash_id %s", mf.Slug, first.App, first.TrashID)
	}

	scores := make([]scoreEntry, 0, len(mf.Scores))
	for _, s := range mf.Scores {
		scores = append(scores, scoreEntry(s))
	}

	rf := resolvedFormat{Slug: mf.Slug, Name: firstCF.Name, TrashIDs: mf.Apps, Scores: scores, Group: mf.Group}
	for _, mc := range mf.Conditions {
		rc, err := resolveCondition(idx, mf.Apps, mc)
		if err != nil {
			return resolvedFormat{}, fmt.Errorf("format %q: %w", mf.Slug, err)
		}
		rf.Conditions = append(rf.Conditions, rc)
	}
	return rf, nil
}

// loadManifestFamily reads one manifest/<family>.json file.
func loadManifestFamily(path string) ([]manifestFormat, error) {
	doc, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var formats []manifestFormat
	if err := json.Unmarshal(doc, &formats); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return formats, nil
}

// GenerateAll reads every manifest/<family>.json under manifestDir, resolves
// each against the corpus under corpusDir, and renders the loader's JSON
// shape for each family, keyed by output filename (e.g. "unwanted.json").
func GenerateAll(corpusDir, manifestDir string) (map[string][]byte, error) {
	idx, err := loadCorpusIndexes(corpusDir)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(manifestDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", manifestDir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	out := make(map[string][]byte, len(names))
	for _, name := range names {
		manifest, err := loadManifestFamily(filepath.Join(manifestDir, name))
		if err != nil {
			return nil, err
		}
		resolved := make([]resolvedFormat, 0, len(manifest))
		for _, mf := range manifest {
			rf, err := resolveFormat(idx, mf)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			resolved = append(resolved, rf)
		}
		out[name] = renderFamily(resolved)
	}
	return out, nil
}
