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

package quality

import (
	"encoding/json"
	"fmt"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// ProfileSeed is the embedded JSON shape of one built-in QualityProfile,
// decoded from pkg/quality/catalogue/data/profiles/*.json. Its fields mirror
// catalogv1alpha1.QualityProfileSpec directly so DecodeProfileSeeds can
// build a real QualityProfile and hand it to FromCRD without a second
// resolution path.
type ProfileSeed struct {
	Name string
	Spec catalogv1alpha1.QualityProfileSpec
}

// profileSeedJSON is the on-disk shape. Source is provenance metadata only
// (which upstream TRaSH profile this was transcribed from, and any
// deliberate deviation -- see anime-web-1080p.json's cutoff override); it is
// never resolved into Spec.
type profileSeedJSON struct {
	Name                  string                            `json:"name"`
	Source                string                            `json:"_source"`
	MediaKind             catalogv1alpha1.ProfileMediaKind  `json:"mediaKind"`
	Tiers                 []catalogv1alpha1.Tier            `json:"tiers"`
	Cutoff                string                            `json:"cutoff"`
	UpgradeAllowed        *bool                             `json:"upgradeAllowed"`
	MinFormatScore        int32                             `json:"minFormatScore"`
	CutoffFormatScore     int32                             `json:"cutoffFormatScore"`
	MinUpgradeFormatScore int32                             `json:"minUpgradeFormatScore"`
	ScoreSet              catalogv1alpha1.ScoreSet          `json:"scoreSet"`
	EnabledFormatGroups   []string                          `json:"enabledFormatGroups"`
	FormatScores          []catalogv1alpha1.FormatScore     `json:"formatScores"`
	Language              string                            `json:"language"`
	ProperPolicy          catalogv1alpha1.ProperPolicy      `json:"properPolicy"`
	SizeTable             catalogv1alpha1.SizeTable         `json:"sizeTable"`
	PreferredProtocol     catalogv1alpha1.PreferredProtocol `json:"preferredProtocol"`
}

// DecodeProfileSeeds parses one data/profiles/*.json document (a JSON array
// of profile seeds).
func DecodeProfileSeeds(doc []byte) ([]ProfileSeed, error) {
	var raw []profileSeedJSON
	if err := json.Unmarshal(doc, &raw); err != nil {
		return nil, fmt.Errorf("decode profile seeds: %w", err)
	}
	seeds := make([]ProfileSeed, 0, len(raw))
	for _, r := range raw {
		seeds = append(seeds, ProfileSeed{Name: r.Name, Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: r.MediaKind, Tiers: r.Tiers, Cutoff: r.Cutoff, UpgradeAllowed: r.UpgradeAllowed,
			MinFormatScore: r.MinFormatScore, CutoffFormatScore: r.CutoffFormatScore,
			MinUpgradeFormatScore: r.MinUpgradeFormatScore, ScoreSet: r.ScoreSet,
			EnabledFormatGroups: r.EnabledFormatGroups, FormatScores: r.FormatScores,
			Language: r.Language, ProperPolicy: r.ProperPolicy, SizeTable: r.SizeTable,
			PreferredProtocol: r.PreferredProtocol,
		}})
	}
	return seeds, nil
}

// BuiltinProfiles decodes every embedded profile seed, resolves each against
// cat with FromCRD, and returns them keyed by name -- the "13 built-in
// profiles ... loaded at init" mechanism spec §7/§9 describes. A later
// phase's catalogarr controller calls this at startup to create the
// QualityProfile CRs; this task only builds and validates the map.
func BuiltinProfiles(cat *catalogue.Catalogue) (map[string]Profile, []error) {
	entries, err := catalogue.ProfileFS().ReadDir("data/profiles")
	if err != nil {
		return nil, []error{err}
	}
	var errs []error
	out := make(map[string]Profile, len(entries))
	for _, e := range entries {
		doc, err := catalogue.ProfileFS().ReadFile("data/profiles/" + e.Name())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		seeds, err := DecodeProfileSeeds(doc)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		for _, seed := range seeds {
			p := &catalogv1alpha1.QualityProfile{Spec: seed.Spec}
			p.Name = seed.Name
			prof, perrs := FromCRD(p, cat)
			for _, pe := range perrs {
				errs = append(errs, fmt.Errorf("%s: %w", seed.Name, pe))
			}
			out[seed.Name] = prof
		}
	}
	return out, errs
}
