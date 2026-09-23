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

package decision_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// crdDefaultedSpec builds the QualityProfileSpec an apiserver would hand back
// for a QualityProfile that sets only the three required fields (mediaKind,
// tiers, cutoff) and leaves every optional one out.
//
// Every value here is asserted against the generated CRD in
// config/crd/bases/catalog.clustarr.io_qualityprofiles.yaml by
// requireMatchesCRDDefaults below, so this cannot quietly drift into being a
// hand-picked permissive profile -- which is the whole point: the defect this
// file pins is one that only a DEFAULTED profile shows.
func crdDefaultedSpec(tiers []catalogv1alpha1.Tier, cutoff string) catalogv1alpha1.QualityProfileSpec {
	return catalogv1alpha1.QualityProfileSpec{
		MediaKind:             catalogv1alpha1.ProfileMediaKindVideo,
		Tiers:                 tiers,
		Cutoff:                cutoff,
		BuiltIn:               false,
		UpgradeAllowed:        ptr.To(true),
		MinFormatScore:        0,
		CutoffFormatScore:     10000,
		MinUpgradeFormatScore: 1,
		ScoreSet:              catalogv1alpha1.ScoreSet("default"),
		Language:              "original",
		ProperPolicy:          catalogv1alpha1.ProperPolicy("preferAndUpgrade"),
		SizeTable:             catalogv1alpha1.SizeTable("movie"),
		PreferredProtocol:     catalogv1alpha1.PreferredProtocol("any"),
	}
}

// requireMatchesCRDDefaults reads the generated CRD and proves every value
// crdDefaultedSpec hard-codes really is that field's apiserver default. A
// unit test cannot run the defaulting admission plugin, so this is what makes
// "at CRD defaults" a claim rather than a comment.
func requireMatchesCRDDefaults(t *testing.T) {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "bases",
		"catalog.clustarr.io_qualityprofiles.yaml"))
	require.NoError(t, err)

	var crd apiextv1.CustomResourceDefinition
	require.NoError(t, yaml.Unmarshal(doc, &crd))
	require.Len(t, crd.Spec.Versions, 1)
	props := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties

	for field, want := range map[string]string{
		"builtIn":               "false",
		"upgradeAllowed":        "true",
		"minFormatScore":        "0",
		"cutoffFormatScore":     "10000",
		"minUpgradeFormatScore": "1",
		"scoreSet":              `"default"`,
		"language":              `"original"`,
		"properPolicy":          `"preferAndUpgrade"`,
		"sizeTable":             `"movie"`,
		"preferredProtocol":     `"any"`,
	} {
		p, ok := props[field]
		require.True(t, ok, "QualityProfileSpec has no %q property", field)
		require.NotNil(t, p.Default, "QualityProfileSpec.%s carries no CRD default", field)
		require.JSONEq(t, want, string(p.Default.Raw),
			"crdDefaultedSpec is stale: QualityProfileSpec.%s defaults differently now", field)
	}
}

// defaultProfile resolves crdDefaultedSpec through the real quality.FromCRD
// and the real embedded TRaSH catalogue -- no synthetic catalogue, because
// the -10000 half of this defect lives in an embedded format.
func defaultProfile(t *testing.T) (quality.Profile, *catalogue.Catalogue) {
	t.Helper()
	requireMatchesCRDDefaults(t)

	cat := catalogue.LoadedCatalogue()
	qp := &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "crd-defaults"},
		Spec: crdDefaultedSpec([]catalogv1alpha1.Tier{
			{Name: "HD", Qualities: []string{"Bluray-1080p", "WEBDL-1080p"}},
		}, "HD"),
	}
	p, errs := quality.FromCRD(qp, cat)
	require.Empty(t, errs)
	return p, cat
}

func defaultOptions() decision.Options {
	return decision.Options{
		UserInvoked:      true,
		ProtocolsEnabled: map[string]bool{"torrent": true, "usenet": true},
	}
}

// englishBluray is an ordinary English 1080p Bluray release. SizeBytes is 0
// so the size model stays out of the way (sizeRejections: "a release with
// unknown size (0) is never rejected"); every other check runs for real.
func englishBluray() common.ReleaseInfo {
	return common.ReleaseInfo{
		GUID:       "tracker:1",
		IndexerRef: "tracker",
		Title:      "Arrival.2016.1080p.BluRay.x264-GROUP",
		Protocol:   common.ProtocolTorrent,
	}
}

// TestEvaluateAtCRDDefaultsApprovesAnEnglishRelease is THE regression test for
// the language-vocabulary defect: a QualityProfile left entirely at its CRD
// defaults (language "original", minFormatScore 0) approved NOTHING, because
// catalogarr handed pkg/decision the BCP-47 tag from
// Movie.status.metadata.originalLanguage ("en") while both consumers of that
// value speak Radarr's English display names ("English"):
//
//   - languageRejection's "original" branch compared "en" against
//     ParsedRelease.Languages ["English"] and emitted ReasonWantedLanguage; and
//   - the embedded language-not-original custom format, a NEGATED "contains
//     Original", matched for the same reason and scored -10000, which
//     minFormatScore 0 then rejected as well.
//
// Its absence is what let the defect ship: every existing test either set
// language "any", set minFormatScore -10000, or stored a display name in the
// tag-typed CRD field.
func TestEvaluateAtCRDDefaultsApprovesAnEnglishRelease(t *testing.T) {
	p, cat := defaultProfile(t)
	tg := decision.Target{
		Kind:                common.MediaKindMovie,
		Available:           true,
		OriginalLanguageTag: "en", // exactly what Movie.status.metadata.originalLanguage carries
	}

	ds := decision.Evaluate(context.Background(), tg, p, cat, []common.ReleaseInfo{englishBluray()}, defaultOptions())
	require.Len(t, ds, 1)
	require.True(t, ds[0].Approved,
		"an English release of an English movie must be approved by a profile at CRD defaults; rejected with %+v (score %d, matched %v)",
		ds[0].Rejections, ds[0].Score, ds[0].Matched)
	require.NotContains(t, ds[0].Matched, "language-not-original",
		"original language \"en\" must resolve to English before the Original condition is evaluated")
}

// TestEvaluateOriginalLanguageVocabulary walks the cases the language table
// cannot answer directly. It asserts on the two things the ORIGINAL-language
// machinery produces -- the ReasonWantedLanguage rejection and the
// language-not-original custom format -- rather than on blanket approval,
// because a non-English release also trips the always-active
// language-not-english format (-10000) for reasons that have nothing to do
// with this defect, and an approval assertion would silently be measuring
// that instead.
func TestEvaluateOriginalLanguageVocabulary(t *testing.T) {
	cases := []struct {
		name          string
		tag           string
		relTitle      string
		wantRejection bool
		wantFormat    bool
		why           string
	}{
		{
			// es-419 rather than pt-BR only because pkg/release's language
			// table detects SPANISH out of a title and has no Portuguese
			// token at all (pkg/release/language.go's languageGroups). The
			// tag-side half of pt-BR is pinned directly below, in
			// TestRegionSubtagsResolveThroughTheirPrimarySubtag.
			name:     "region subtag resolves through its primary subtag",
			tag:      "es-419",
			relTitle: "Pelicula.2016.SPANISH.1080p.BluRay.x264-GROUP",
			why:      "es-419 is BCP-47 for Latin-American Spanish; the region subtag must not defeat the lookup",
		},
		{
			name:     "unknown tag is not a rejection",
			tag:      "cn", // TMDB emits this for Cantonese; it is not ISO-639-1
			relTitle: "Movie.2016.1080p.BluRay.x264-GROUP",
			why:      "a tag the table cannot resolve means the original language is UNKNOWN, not wrong",
		},
		{
			name:     "empty tag is not a rejection",
			tag:      "",
			relTitle: "Movie.2016.1080p.BluRay.x264-GROUP",
			why:      "metadata has not been fetched yet; there is no fact to reject against",
		},
		{
			name:          "a resolvable original language is still enforced",
			tag:           "ja",
			relTitle:      "Movie.2016.1080p.BluRay.x264-GROUP",
			wantRejection: true,
			wantFormat:    true,
			why:           "an English-only release of a Japanese film must still be rejected -- the fix must not disable the check",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, cat := defaultProfile(t)
			rel := englishBluray()
			rel.Title = tc.relTitle
			tg := decision.Target{
				Kind:                common.MediaKindMovie,
				Available:           true,
				OriginalLanguageTag: tc.tag,
			}
			ds := decision.Evaluate(context.Background(), tg, p, cat, []common.ReleaseInfo{rel}, defaultOptions())
			require.Len(t, ds, 1)

			var gotRejection bool
			for _, r := range ds[0].Rejections {
				if strings.Contains(r.Reason, decision.ReasonWantedLanguage.Code) {
					gotRejection = true
				}
			}
			require.Equal(t, tc.wantRejection, gotRejection,
				"%s: rejections %+v", tc.why, ds[0].Rejections)
			if tc.wantFormat {
				require.Contains(t, ds[0].Matched, "language-not-original", "%s", tc.why)
			} else {
				require.NotContains(t, ds[0].Matched, "language-not-original",
					"%s: matched %v, score %d", tc.why, ds[0].Matched, ds[0].Score)
			}
		})
	}
}

// TestEvaluateApprovesAReleaseInTheItemsOwnOriginalLanguage is the
// decision-level (end-to-end) regression test for the product decision
// recorded in .superpowers/sdd/lang-policy/brief.md: "a release in the
// item's own original language must be approvable by a quality profile left
// at its CRD defaults, whatever that language is." It exercises the same
// real embedded TRaSH catalogue and real CRD-defaulted profile as
// TestEvaluateAtCRDDefaultsApprovesAnEnglishRelease, so the fix is proven
// against production data, not a synthetic catalogue.
//
// Before this fix a Japanese release of a Japanese-original film scored
// -10000 from the always-active language-not-english custom format (a
// NEGATED "contains English" condition, data/formats/language.json), which
// the CRD's minFormatScore: 0 then rejected -- even though the vocabulary
// defect fixed at 6ae2c8b already let language-not-original stand down
// correctly for that very same release.
func TestEvaluateApprovesAReleaseInTheItemsOwnOriginalLanguage(t *testing.T) {
	cases := []struct {
		name         string
		tag          string
		relTitle     string
		wantApproved bool
		why          string
	}{
		{
			name:         "Japanese release of a Japanese-original movie is approved",
			tag:          "ja",
			relTitle:     "Movie.2016.JAPANESE.1080p.BluRay.x264-GROUP",
			wantApproved: true,
			why:          "the release is in the item's own original language -- the whole point of this fix",
		},
		{
			name:         "English release of an English-original movie is still approved",
			tag:          "en",
			relTitle:     "Arrival.2016.1080p.BluRay.x264-GROUP",
			wantApproved: true,
			why:          "must not regress the case fixed at 6ae2c8b",
		},
		{
			name:         "French release of an English-original movie is still rejected",
			tag:          "en",
			relTitle:     "Movie.2016.FRENCH.1080p.BluRay.x264-GROUP",
			wantApproved: false,
			why:          "the product decision allows non-English approvals only when English is not the original language -- not a blanket disable of the format",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, cat := defaultProfile(t)
			rel := englishBluray()
			rel.Title = tc.relTitle
			tg := decision.Target{
				Kind:                common.MediaKindMovie,
				Available:           true,
				OriginalLanguageTag: tc.tag,
			}
			ds := decision.Evaluate(context.Background(), tg, p, cat, []common.ReleaseInfo{rel}, defaultOptions())
			require.Len(t, ds, 1)
			require.Equal(t, tc.wantApproved, ds[0].Approved, "%s; rejected with %+v (score %d, matched %v)",
				tc.why, ds[0].Rejections, ds[0].Score, ds[0].Matched)
		})
	}
}

// TestRegionSubtagsResolveThroughTheirPrimarySubtag pins the tag half of the
// region-subtag case against the language table itself, independently of what
// pkg/release can parse out of a title. Radarr's table carries "Portuguese
// (Brazil)" and "Spanish (Latino)" as separate display names with NO tag of
// their own (languages.go), so a provider's "pt-BR" has to resolve through
// its primary subtag or not at all -- and "not at all" would have meant a
// Brazilian film rejecting every release.
func TestRegionSubtagsResolveThroughTheirPrimarySubtag(t *testing.T) {
	for tag, want := range map[string]string{
		"pt-BR":      "Portuguese",
		"es-419":     "Spanish",
		"en-US":      "English",
		"zh-Hant-TW": "Chinese",
	} {
		got, ok := catalogue.LanguageName(tag)
		require.True(t, ok, "%s must resolve; an unresolvable original language constrains nothing, which silently disables the check", tag)
		require.Equal(t, want, got, "%s", tag)
	}

	_, ok := catalogue.LanguageName("cn")
	require.False(t, ok, "cn is not ISO-639-1; it must report unresolvable rather than resolving to something plausible")
}

// TestEvaluateISO6392And3TagsMatchISO6391 is F-2b's regression test: TVDB
// hands back ISO 639-3 ("eng", "jpn") and ffprobe hands back ISO 639-2
// bibliographic codes ("fre") for exactly this same field, and before
// pkg/lang.Normalize was wired into originalLanguageName, none of them were
// in catalogue's language table at all. That sent every one of them down the
// same "original language is unknown" fail-open path as genuine garbage --
// which for a QualityProfile at CRD defaults with a non-English-original
// item meant the -10000 language-not-original format stopped applying
// silently, exactly the shape of the two prior incidents this task exists to
// stop recurring.
//
// Each case runs decision.Evaluate twice, once with the ISO-639-1 tag and
// once with its ISO 639-2/3 equivalent, for the identical release, and
// requires the two Decisions to agree on Approved, Matched and Rejections --
// not merely both non-empty, so a normalization that resolves to the wrong
// language would still be caught.
func TestEvaluateISO6392And3TagsMatchISO6391(t *testing.T) {
	cases := []struct {
		name       string
		iso6391    string
		iso6392or3 string
		relTitle   string
	}{
		{
			name:       "iso 639-3 english original, english release approved",
			iso6391:    "en",
			iso6392or3: "eng",
			relTitle:   "Arrival.2016.1080p.BluRay.x264-GROUP",
		},
		{
			name:       "iso 639-3 japanese original, english-only release still rejected",
			iso6391:    "ja",
			iso6392or3: "jpn",
			relTitle:   "Movie.2016.1080p.BluRay.x264-GROUP",
		},
		{
			name:       "iso 639-3 japanese original, japanese release approved",
			iso6391:    "ja",
			iso6392or3: "jpn",
			relTitle:   "Movie.2016.JAPANESE.1080p.BluRay.x264-GROUP",
		},
		{
			name:       "iso 639-2/b french original, french release approved",
			iso6391:    "fr",
			iso6392or3: "fre",
			relTitle:   "Film.2016.FRENCH.1080p.BluRay.x264-GROUP",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, cat := defaultProfile(t)
			rel := englishBluray()
			rel.Title = tc.relTitle

			evalWith := func(tag string) []decision.Decision {
				tg := decision.Target{
					Kind:                common.MediaKindMovie,
					Available:           true,
					OriginalLanguageTag: tag,
				}
				return decision.Evaluate(context.Background(), tg, p, cat, []common.ReleaseInfo{rel}, defaultOptions())
			}

			want := evalWith(tc.iso6391)
			got := evalWith(tc.iso6392or3)
			require.Len(t, want, 1)
			require.Len(t, got, 1)

			require.Equal(t, want[0].Approved, got[0].Approved,
				"%s (%s) and %s (%s) must reach the same Approved verdict; want rejections %+v, got rejections %+v",
				tc.iso6391, tc.name, tc.iso6392or3, tc.name, want[0].Rejections, got[0].Rejections)
			require.Equal(t, want[0].Matched, got[0].Matched,
				"%s and %s must match the same custom formats", tc.iso6391, tc.iso6392or3)
			require.Equal(t, want[0].Rejections, got[0].Rejections,
				"%s and %s must carry the same rejections", tc.iso6391, tc.iso6392or3)
		})
	}
}
