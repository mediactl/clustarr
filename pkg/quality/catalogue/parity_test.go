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

package catalogue_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// corpusSpecification is one *arr CustomFormatSpecification as the vendored
// corpus JSON shapes it.
type corpusSpecification struct {
	Implementation string          `json:"implementation"`
	Name           string          `json:"name"`
	Negate         bool            `json:"negate"`
	Required       bool            `json:"required"`
	Fields         json.RawMessage `json:"fields"`
}

// corpusFormatByTrashID scans testdata/trash/docs/json/<app>/cf for the file
// whose trash_id matches id and returns its raw specifications.
func corpusFormatByTrashID(t *testing.T, app, id string) []corpusSpecification {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "testdata", "trash", "docs", "json", app, "cf")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		doc, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		var raw struct {
			TrashID        string                `json:"trash_id"`
			Specifications []corpusSpecification `json:"specifications"`
		}
		require.NoError(t, json.Unmarshal(doc, &raw))
		if raw.TrashID == id {
			return raw.Specifications
		}
	}
	t.Fatalf("no corpus file under %s has trash_id %s", dir, id)
	return nil
}

// TestEveryEmbeddedFormatMatchesItsCorpusSource is the correctness backstop
// the controller amendment requires in place of hand-verifying each
// transcription: for every embedded Format that carries a trash_id, every
// ReleaseTitle/ReleaseGroup condition's compiled Pattern.String() must equal
// the corpus's fields.value for a specification of the same Name, so a
// merged (two-app) Format like web-tier-01 cannot silently drop or duplicate
// a group, and a hand-transcription typo cannot pass silently.
//
// A Format only lists an app in TrashIDs when its embedded Conditions were
// actually verified against that app's own corpus copy; where the two apps'
// copies of the "same" custom format diverge (e.g. web-tier-01's release
// group lists, or vrv's ReleaseTitle pattern differing only in letter case),
// only the app the Conditions were transcribed from is listed -- see
// load_test.go's family tests and the task report for the specific cases.
func TestEveryEmbeddedFormatMatchesItsCorpusSource(t *testing.T) {
	all := loadAllEmbeddedFormats(t) // defined in catalogue_test.go (Step 24)
	for slug, f := range all {
		for app, id := range f.TrashIDs {
			t.Run(slug+"/"+app, func(t *testing.T) {
				corpus := corpusFormatByTrashID(t, app, id)
				for _, cond := range f.Conditions {
					if cond.Pattern == nil {
						continue // non-regex kind, nothing to diff against fields.value
					}
					found := false
					for _, cs := range corpus {
						var fv struct {
							Value string `json:"value"`
						}
						_ = json.Unmarshal(cs.Fields, &fv)
						if cs.Name == cond.Name && fv.Value == cond.Pattern.String() {
							found = true
							break
						}
					}
					require.Truef(t, found, "embedded condition %q on %s has no byte-identical match in the %s corpus file (trash_id %s)", cond.Name, slug, app, id)
				}
			})
		}
	}
}

// condImplementation maps a catalogue.CondKind to the *arr specification
// "implementation" name it corresponds to in the corpus, so a corpus
// specification and an embedded Condition can be matched up by kind.
var condImplementation = map[catalogue.CondKind]string{
	catalogue.CondReleaseTitle: "ReleaseTitleSpecification",
	catalogue.CondReleaseGroup: "ReleaseGroupSpecification",
	catalogue.CondSource:       "SourceSpecification",
	catalogue.CondResolution:   "ResolutionSpecification",
	catalogue.CondModifier:     "QualityModifierSpecification",
	catalogue.CondLanguage:     "LanguageSpecification",
	catalogue.CondIndexerFlag:  "IndexerFlagSpecification",
	catalogue.CondReleaseType:  "ReleaseTypeSpecification",
	catalogue.CondEdition:      "EditionSpecification",
	catalogue.CondSize:         "SizeSpecification",
	catalogue.CondYear:         "YearSpecification",
}

// Per-app numeric-value translation tables for the non-pattern condition
// kinds this package actually embeds (Source, Modifier, ReleaseType;
// Resolution is already the same integer in both apps and needs no table;
// Language is translated via catalogue.LanguageByID; no embedded format
// uses IndexerFlag). Radarr and Sonarr use *different* internal numeric
// encodings for the same SourceSpecification/QualityModifierSpecification
// meaning (verified by scanning the vendored corpus while building this
// test) -- notably Sonarr has no QualityModifierSpecification usage at all
// and instead encodes "remux" as a distinct Source value ("Bluray Remux"),
// which is exactly the divergence documented in knownGaps below.
var (
	radarrSourceByID = map[float64]common.Source{
		5: common.SourceDVD, 7: common.SourceWebDL, 8: common.SourceWebRip, 9: common.SourceBluray,
	}
	sonarrSourceByID = map[float64]common.Source{
		3: common.SourceWebDL, 4: common.SourceWebRip, 5: common.SourceDVD, 6: common.SourceBluray,
	}
	radarrModifierByID = map[float64]common.Modifier{5: common.ModifierRemux}
	releaseTypeByID    = map[float64]common.ReleaseType{3: common.ReleaseTypeSeasonPack}
)

// appNotRepresented is a knownGap.name sentinel meaning "this format's
// TrashIDs deliberately omits this app entirely" (as opposed to naming one
// specific corpus condition this task does not embed).
const appNotRepresented = "*"

const (
	reasonSonarrRemuxIsSourceNotModifier = "Sonarr encodes \"remux\" as a distinct SourceSpecification value (\"Bluray Remux\"), not a separate QualityModifierSpecification the way Radarr does (Sonarr has no QualityModifierSpecification usage anywhere in the corpus); every anime BD tier format's embedded Source conditions (Bluray/DVD only) already came from Radarr's copy and carry no remux distinction in either app, so this gap changes no matching behavior -- see pkg/quality/catalogue/parity_test.go's per-app Source table comment."
	reasonGenericWebSource               = "Sonarr's generic \"WEB\" SourceSpecification (value 1, meaning WEBDL-or-WEBRip-unspecified) has no common.Source equivalent -- only SourceWebDL/SourceWebRip exist -- and release.ParsedRelease.Quality.Source is always one specific value in practice, so omitting it is strictly more correct for this model, not a loss."
)

// knownGap is one reviewed, explicitly documented divergence between this
// package's embedded catalogue and the vendored corpus. Every gap
// TestEveryEmbeddedFormatIsCompleteRelativeToItsCorpusSource finds must be
// listed here with its one-line reason, or the test fails: this is what
// keeps "curated subset" from silently drifting into "incomplete
// transcription" as the catalogue grows.
type knownGap struct {
	slug, app, name, reason string
}

var knownGaps = []knownGap{
	// Sonarr's "Bluray Remux" Source value, subsumed by Radarr's Source+
	// Modifier split -- see reasonSonarrRemuxIsSourceNotModifier.
	{"anime-bd-tier-01", "sonarr", "Bluray Remux", reasonSonarrRemuxIsSourceNotModifier},
	{"anime-bd-tier-02", "sonarr", "Bluray Remux", reasonSonarrRemuxIsSourceNotModifier},
	{"anime-bd-tier-03", "sonarr", "Bluray Remux", reasonSonarrRemuxIsSourceNotModifier},
	{"anime-bd-tier-04", "sonarr", "Bluray Remux", reasonSonarrRemuxIsSourceNotModifier},
	{"anime-bd-tier-05", "sonarr", "Bluray Remux", reasonSonarrRemuxIsSourceNotModifier},
	{"anime-bd-tier-06", "sonarr", "Bluray Remux", reasonSonarrRemuxIsSourceNotModifier},
	{"anime-bd-tier-07", "sonarr", "Bluray Remux", reasonSonarrRemuxIsSourceNotModifier},
	{"anime-bd-tier-08", "sonarr", "Bluray Remux", reasonSonarrRemuxIsSourceNotModifier},

	// Sonarr's generic "WEB" Source value -- see reasonGenericWebSource.
	{"anime-web-tier-01", "sonarr", "WEB", reasonGenericWebSource},
	{"anime-web-tier-02", "sonarr", "WEB", reasonGenericWebSource},
	{"anime-web-tier-03", "sonarr", "WEB", reasonGenericWebSource},
	{"anime-web-tier-04", "sonarr", "WEB", reasonGenericWebSource},
	{"anime-web-tier-05", "sonarr", "WEB", reasonGenericWebSource},
	{"anime-web-tier-06", "sonarr", "WEB", reasonGenericWebSource},
	{"anime-cr", "sonarr", "WEB", reasonGenericWebSource},
	{"anime-abema", "sonarr", "WEB", reasonGenericWebSource},
	{"anime-adn", "sonarr", "WEB", reasonGenericWebSource}, //nolint:misspell // "adn" is ADN (Anime Digital Network), a real streaming-service slug, not a typo for "and"
	{"anime-b-global", "sonarr", "WEB", reasonGenericWebSource},
	{"anime-bilibili", "sonarr", "WEB", reasonGenericWebSource},
	{"anime-funi", "sonarr", "WEB", reasonGenericWebSource},
	{"anime-hidive", "sonarr", "WEB", reasonGenericWebSource},
	{"anime-wkn", "sonarr", "WEB", reasonGenericWebSource},
	// anime-dsnp, anime-nf and anime-amzn are NOT listed here: their real
	// upstream CFs only ever carry WEBDL/WEBRIP SourceSpecifications, never
	// the generic "WEB" value, so there is no gap to document for them
	// (verified while building this table, not assumed).

	// Cross-app release-group list divergence: Radarr's and Sonarr's own
	// copies of these formats carry genuinely different release-group
	// lists (not just a different Source encoding), so only the app
	// actually transcribed is claimed in TrashIDs at all -- see
	// TestFormatDoesNotClaimAnUnrepresentedApp below, which verifies each
	// of these stays true.
	{"web-tier-01", "sonarr", appNotRepresented, "13 of 22 Radarr release-groups are common with Sonarr's own WEB Tier 01 copy (9 Radarr-only, 9 Sonarr-only); only Radarr's list is embedded"},
	{"web-tier-02", "sonarr", appNotRepresented, "Radarr's and Sonarr's WEB Tier 02 release-group lists differ; only Radarr's is embedded"},
	{"web-tier-03", "sonarr", appNotRepresented, "Radarr's and Sonarr's WEB Tier 03 release-group lists differ; only Radarr's is embedded"},
	{"vrv", "sonarr", appNotRepresented, "Sonarr's VRV ReleaseTitle pattern differs from Radarr's only in letter case (\\b(VRV)\\b vs \\b(vrv)\\b); only Radarr's is embedded"},
	{"anime-lq-groups", "sonarr", appNotRepresented, "4 of ~94 release-group patterns differ between apps (trailing \\b vs $ anchors: Ari, DaddySubs, Mites, SAD); only Radarr's is embedded"},
}

// gapReason looks up whether (slug, app, name) is a documented knownGap,
// returning its reason and marking it used.
func gapReason(used map[int]bool, slug, app, name string) (string, bool) {
	for i, g := range knownGaps {
		if g.slug == slug && g.app == app && g.name == name {
			used[i] = true
			return g.reason, true
		}
	}
	return "", false
}

// TestEveryEmbeddedFormatIsCompleteRelativeToItsCorpusSource extends
// TestEveryEmbeddedFormatMatchesItsCorpusSource's byte-fidelity check
// (embedded conditions do not silently diverge from the corpus) with a
// completeness check (nothing is silently dropped or fabricated): for every
// embedded Format and every app listed in its TrashIDs, the multiset of
// (implementation, name) pairs must equal the corpus's exactly, except for
// gaps listed in knownGaps; every condition present on both sides must
// agree on Required/Negate; and for the non-regex kinds this package
// embeds (Source, Modifier, Language, ReleaseType -- Resolution's raw
// value is already the same integer as common.Resolution* in both apps)
// the translated value must also agree.
func TestEveryEmbeddedFormatIsCompleteRelativeToItsCorpusSource(t *testing.T) {
	all := loadAllEmbeddedFormats(t)
	used := make(map[int]bool, len(knownGaps))

	for slug, f := range all {
		for app, id := range f.TrashIDs {
			t.Run(slug+"/"+app, func(t *testing.T) {
				corpus := corpusFormatByTrashID(t, app, id)

				type key struct{ impl, name string }
				corpusByKey := make(map[key]corpusSpecification, len(corpus))
				for _, cs := range corpus {
					corpusByKey[key{cs.Implementation, cs.Name}] = cs
				}
				embeddedByKey := make(map[key]catalogue.Condition, len(f.Conditions))
				for _, cond := range f.Conditions {
					impl, ok := condImplementation[cond.Kind]
					require.Truef(t, ok, "condition %q on %s has no corpus implementation mapping for kind %q -- extend condImplementation", cond.Name, slug, cond.Kind)
					embeddedByKey[key{impl, cond.Name}] = cond
				}

				// Every corpus condition must either be embedded or be a
				// documented knownGap.
				for k := range corpusByKey {
					if _, ok := embeddedByKey[k]; ok {
						continue
					}
					reason, documented := gapReason(used, slug, app, k.name)
					require.Truef(t, documented, "corpus condition %q (%s) on %s/%s is not embedded and not in knownGaps -- either embed it or add a knownGaps entry with a one-line reason", k.name, k.impl, slug, app)
					t.Logf("documented gap: %s/%s %q: %s", slug, app, k.name, reason)
				}

				// Every embedded condition must exist in the corpus -- no
				// fabricated conditions.
				for k := range embeddedByKey {
					_, ok := corpusByKey[k]
					require.Truef(t, ok, "embedded condition %q (%s) on %s/%s does not exist in the %s corpus file (trash_id %s)", k.name, k.impl, slug, app, app, id)
				}

				// Conditions present on both sides must agree on
				// Required/Negate, and (for the kinds this package embeds)
				// on the translated value.
				for k, cond := range embeddedByKey {
					cs, ok := corpusByKey[k]
					if !ok {
						continue // already reported above
					}
					require.Equalf(t, cs.Required, cond.Required, "%s/%s condition %q: Required mismatch", slug, app, k.name)
					require.Equalf(t, cs.Negate, cond.Negate, "%s/%s condition %q: Negate mismatch", slug, app, k.name)
					checkConditionValue(t, slug, app, k.name, cond, cs)
				}
			})
		}
	}

	for i, g := range knownGaps {
		require.Truef(t, used[i] || g.name == appNotRepresented, "stale knownGaps entry never matched a real gap: %s/%s %q -- remove it or the underlying condition came back", g.slug, g.app, g.name)
	}
}

// checkConditionValue compares an embedded Condition's typed value against
// the corpus specification's raw fields.value for the non-pattern kinds
// this package's embedded data actually uses. Source/Modifier are
// app-specific (see the per-app tables above); Language goes through
// catalogue.LanguageByID; ReleaseType and Resolution are the same encoding
// in both apps.
func checkConditionValue(t *testing.T, slug, app, name string, cond catalogue.Condition, cs corpusSpecification) {
	t.Helper()
	switch cond.Kind {
	case catalogue.CondReleaseTitle, catalogue.CondReleaseGroup:
		return // covered by TestEveryEmbeddedFormatMatchesItsCorpusSource
	case catalogue.CondSource:
		var fv struct {
			Value float64 `json:"value"`
		}
		require.NoError(t, json.Unmarshal(cs.Fields, &fv))
		table := radarrSourceByID
		if app == "sonarr" {
			table = sonarrSourceByID
		}
		want, ok := table[fv.Value]
		require.Truef(t, ok, "%s/%s condition %q: no %s Source value table entry for corpus value %v -- extend the per-app table", slug, app, name, app, fv.Value)
		require.Equalf(t, want, cond.Source, "%s/%s condition %q: Source value mismatch", slug, app, name)
	case catalogue.CondModifier:
		var fv struct {
			Value float64 `json:"value"`
		}
		require.NoError(t, json.Unmarshal(cs.Fields, &fv))
		require.NotEqualf(t, "sonarr", app, "%s/%s condition %q: Sonarr has no QualityModifierSpecification usage in the corpus; this should be unreachable", slug, app, name)
		want, ok := radarrModifierByID[fv.Value]
		require.Truef(t, ok, "%s/%s condition %q: no Modifier value table entry for corpus value %v -- extend radarrModifierByID", slug, app, name, fv.Value)
		require.Equalf(t, want, cond.Modifier, "%s/%s condition %q: Modifier value mismatch", slug, app, name)
	case catalogue.CondResolution:
		var fv struct {
			Value int32 `json:"value"`
		}
		require.NoError(t, json.Unmarshal(cs.Fields, &fv))
		require.Equalf(t, fv.Value, cond.Resolution, "%s/%s condition %q: Resolution value mismatch", slug, app, name)
	case catalogue.CondLanguage:
		var fv struct {
			Value int32 `json:"value"`
		}
		require.NoError(t, json.Unmarshal(cs.Fields, &fv))
		want, ok := catalogue.LanguageByID(fv.Value)
		require.Truef(t, ok, "%s/%s condition %q: no languageByID entry for corpus language id %d -- extend languages.go", slug, app, name, fv.Value)
		require.Equalf(t, want, cond.Language, "%s/%s condition %q: Language value mismatch", slug, app, name)
	case catalogue.CondReleaseType:
		var fv struct {
			Value float64 `json:"value"`
		}
		require.NoError(t, json.Unmarshal(cs.Fields, &fv))
		want, ok := releaseTypeByID[fv.Value]
		require.Truef(t, ok, "%s/%s condition %q: no releaseTypeByID entry for corpus value %v -- extend the table", slug, app, name, fv.Value)
		require.Equalf(t, want, cond.ReleaseType, "%s/%s condition %q: ReleaseType value mismatch", slug, app, name)
	case catalogue.CondIndexerFlag:
		t.Fatalf("%s/%s condition %q: no embedded format uses IndexerFlag yet -- this branch is untested, extend checkConditionValue before relying on it", slug, app, name)
	case catalogue.CondEdition, catalogue.CondSize, catalogue.CondYear:
		return // accepted no-ops, no value semantics to check
	default:
		t.Fatalf("%s/%s condition %q: unhandled CondKind %q in checkConditionValue", slug, app, name, cond.Kind)
	}
}

// TestFormatDoesNotClaimAnUnrepresentedApp verifies every knownGaps entry
// with name == appNotRepresented stays true: the named app must genuinely
// be absent from that format's TrashIDs. If a future edit adds the app
// back (e.g. to fix the underlying divergence) without removing the
// knownGaps entry, this catches the now-stale documentation.
func TestFormatDoesNotClaimAnUnrepresentedApp(t *testing.T) {
	all := loadAllEmbeddedFormats(t)
	for _, g := range knownGaps {
		if g.name != appNotRepresented {
			continue
		}
		t.Run(fmt.Sprintf("%s/%s", g.slug, g.app), func(t *testing.T) {
			f, ok := all[g.slug]
			require.Truef(t, ok, "knownGaps references unknown slug %q", g.slug)
			require.NotContainsf(t, f.TrashIDs, g.app, "knownGaps says %s does not represent %s (%s), but TrashIDs now includes it -- update or remove this knownGaps entry", g.slug, g.app, g.reason)
		})
	}
}
