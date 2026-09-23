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

package subtitles_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/subtitles"
)

func langKeyPtr(k subtitles.LangKey) *subtitles.LangKey { return &k }

func TestPlan(t *testing.T) {
	tests := []struct {
		name       string
		profile    subtitles.Profile
		audioLangs []string
		existing   []subtitles.Existing
		wantWanted []subtitles.LangKey
		wantCutoff bool
	}{
		{
			name: "cutoff satisfied by an embedded stream",
			profile: subtitles.Profile{
				Languages: []subtitles.ProfileLanguage{
					{Key: "en", Language: "en", HI: subtitles.HIPolicyPrefer},
				},
				Cutoff: langKeyPtr("en"),
			},
			existing:   []subtitles.Existing{{LangKey: "en"}}, // embedded English text stream
			wantWanted: nil,
			wantCutoff: true,
		},
		{
			name: "forced sidecar does not satisfy a non-forced want",
			profile: subtitles.Profile{
				Languages: []subtitles.ProfileLanguage{
					{Key: "en", Language: "en", HI: subtitles.HIPolicyPrefer},
				},
			},
			existing:   []subtitles.Existing{{LangKey: "en:forced"}},
			wantWanted: []subtitles.LangKey{"en"},
			wantCutoff: false,
		},
		{
			name: "HI required is not satisfied by a plain file",
			profile: subtitles.Profile{
				Languages: []subtitles.ProfileLanguage{
					{Key: "en:hi", Language: "en", HI: subtitles.HIPolicyRequired},
				},
			},
			existing:   []subtitles.Existing{{LangKey: "en"}},
			wantWanted: []subtitles.LangKey{"en:hi"},
			wantCutoff: false,
		},
		{
			name: "HI excluded is not satisfied by an HI file",
			profile: subtitles.Profile{
				Languages: []subtitles.ProfileLanguage{
					{Key: "en", Language: "en", HI: subtitles.HIPolicyExcluded},
				},
			},
			existing:   []subtitles.Existing{{LangKey: "en:hi"}},
			wantWanted: []subtitles.LangKey{"en"},
			wantCutoff: false,
		},
		{
			name: "HI prefer is satisfied by an HI file even though the triples differ",
			profile: subtitles.Profile{
				Languages: []subtitles.ProfileLanguage{
					// fr is the cutoff and stays unsatisfied, so the run
					// must fall through to the missing computation instead
					// of short-circuiting in the cutoff loop.
					{Key: "fr", Language: "fr", HI: subtitles.HIPolicyPrefer},
					{Key: "en", Language: "en", HI: subtitles.HIPolicyPrefer},
				},
				Cutoff: langKeyPtr("fr"),
			},
			existing:   []subtitles.Existing{{LangKey: "en:hi"}},
			wantWanted: []subtitles.LangKey{"fr"},
			wantCutoff: false,
		},
		{
			name: "audioExclude suppresses a language because the audio already is that language",
			profile: subtitles.Profile{
				Languages: []subtitles.ProfileLanguage{
					{Key: "es", Language: "es", HI: subtitles.HIPolicyPrefer, AudioExclude: true},
					{Key: "en", Language: "en", HI: subtitles.HIPolicyPrefer},
				},
				// Cutoff pinned away from "es" so the audioExclude
				// suppression under test is the desired-filtering pass,
				// not the (separately covered) cutoff-loop branch.
				Cutoff: langKeyPtr("en"),
			},
			audioLangs: []string{"es"},
			wantWanted: []subtitles.LangKey{"en"},
			wantCutoff: false,
		},
		{
			name: "audioOnlyInclude without the audio language is not wanted at all",
			profile: subtitles.Profile{
				Languages: []subtitles.ProfileLanguage{
					{Key: "en", Language: "en", HI: subtitles.HIPolicyPrefer, AudioOnlyInclude: true},
				},
			},
			audioLangs: nil,
			wantWanted: nil,
			wantCutoff: false,
		},
		{
			name: "audioOnlyInclude with the audio language present is wanted",
			profile: subtitles.Profile{
				Languages: []subtitles.ProfileLanguage{
					{Key: "en", Language: "en", HI: subtitles.HIPolicyPrefer, AudioOnlyInclude: true},
				},
			},
			audioLangs: []string{"en"},
			wantWanted: []subtitles.LangKey{"en"},
			wantCutoff: false,
		},
		{
			name: "region subtags: a pt-BR sidecar does not satisfy a pt want",
			profile: subtitles.Profile{
				Languages: []subtitles.ProfileLanguage{
					{Key: "pt", Language: "pt", HI: subtitles.HIPolicyPrefer},
					{Key: "pt-BR", Language: "pt-BR", HI: subtitles.HIPolicyPrefer},
				},
				Cutoff: langKeyPtr("pt"),
			},
			existing:   []subtitles.Existing{{LangKey: "pt-BR"}},
			wantWanted: []subtitles.LangKey{"pt"},
			wantCutoff: false,
		},
		{
			name: "cutoff unset: any wanted language satisfies it and stops the rest being wanted",
			profile: subtitles.Profile{
				Languages: []subtitles.ProfileLanguage{
					{Key: "en", Language: "en", HI: subtitles.HIPolicyPrefer},
					{Key: "fr", Language: "fr", HI: subtitles.HIPolicyPrefer},
				},
			},
			existing:   []subtitles.Existing{{LangKey: "en"}},
			wantWanted: nil,
			wantCutoff: true,
		},
		{
			name:       "empty profile wants nothing",
			profile:    subtitles.Profile{},
			wantWanted: nil,
			wantCutoff: false,
		},
		{
			name: "an unparseable existing entry is ignored, not fatal",
			profile: subtitles.Profile{
				Languages: []subtitles.ProfileLanguage{
					{Key: "en", Language: "en", HI: subtitles.HIPolicyPrefer},
				},
			},
			existing:   []subtitles.Existing{{LangKey: "en:forced:hi"}}, // ParseLangKey rejects this
			wantWanted: []subtitles.LangKey{"en"},
			wantCutoff: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotWanted, gotCutoff := subtitles.Plan(tt.profile, tt.audioLangs, tt.existing)
			assert.Equal(t, tt.wantWanted, gotWanted)
			assert.Equal(t, tt.wantCutoff, gotCutoff)
		})
	}
}

// TestPlanCutoffNamingUnknownKeyNeverPanics documents that a Cutoff
// pointing at a langKey absent from Languages (a shape the CEL rule
// "cutoff must be one of spec.languages[].key" should already have
// rejected at the API layer) degrades to "cutoff never met" instead of
// panicking, since Plan is pure and must not assume its caller validated
// the profile.
func TestPlanCutoffNamingUnknownKeyNeverPanics(t *testing.T) {
	profile := subtitles.Profile{
		Languages: []subtitles.ProfileLanguage{
			{Key: "en", Language: "en", HI: subtitles.HIPolicyPrefer},
		},
		Cutoff: langKeyPtr("de"),
	}
	wanted, cutoffMet := subtitles.Plan(profile, nil, nil)
	assert.False(t, cutoffMet)
	assert.Equal(t, []subtitles.LangKey{"en"}, wanted)
}
