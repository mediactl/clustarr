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

package providerset_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/providerset"
)

func names(es []providerset.Entry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name)
	}
	return out
}

// NeedsSecrets lists each type's Secret keys from providerset's static
// table; build's TestCapabilityTableMatchesTheClients holds that table to
// each client's own Capabilities().NeedsSecrets.
func TestNeedsSecretsListsEachTypesKeys(t *testing.T) {
	assert.ElementsMatch(t, []string{
		subtitlev1alpha1.ProviderSecretKeyAPIKey,
		subtitlev1alpha1.ProviderSecretKeyUsername,
		subtitlev1alpha1.ProviderSecretKeyPassword,
	}, providerset.NeedsSecrets(subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom))
	assert.Empty(t, providerset.NeedsSecrets(subtitlev1alpha1.SubtitleProviderGestdown))
	assert.Empty(t, providerset.NeedsSecrets(subtitlev1alpha1.SubtitleProviderEmbedded))
	assert.Equal(t, []string{subtitlev1alpha1.ProviderSecretKeyAPIKey}, providerset.NeedsSecrets(subtitlev1alpha1.SubtitleProviderSubDL))
	assert.Equal(t, []string{subtitlev1alpha1.ProviderSecretKeyAPIKey}, providerset.NeedsSecrets(subtitlev1alpha1.SubtitleProviderSubSource))
	assert.Empty(t, providerset.NeedsSecrets(subtitlev1alpha1.SubtitleProviderWhisper))
}

// HIVerifiable -- the one source the SubtitleProvider controller's
// status.hiVerifiable and the fetch worker's HI filter both read -- is true
// for every type with a client and false for one without.
// build's TestCapabilityTableMatchesTheClients holds the table it reads to
// each client's own HIVerifiable().
func TestHIVerifiableIsTrueForEveryClientType(t *testing.T) {
	for _, typ := range []subtitlev1alpha1.SubtitleProviderType{
		subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom,
		subtitlev1alpha1.SubtitleProviderGestdown,
		subtitlev1alpha1.SubtitleProviderSubDL,
		subtitlev1alpha1.SubtitleProviderSubSource,
		subtitlev1alpha1.SubtitleProviderEmbedded,
	} {
		assert.True(t, providerset.HIVerifiable(typ), typ)
	}
	assert.False(t, providerset.HIVerifiable(subtitlev1alpha1.SubtitleProviderWhisper), "no client, nothing to vouch")
}

func TestOrderAppliesTheProfilesProviderList(t *testing.T) {
	entries := []providerset.Entry{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	assert.Equal(t, []string{"a", "b", "c"}, names(providerset.Order(entries, nil)), "no list keeps priority order")
	assert.Equal(t, []string{"c", "a"}, names(providerset.Order(entries, []string{"c", "gone", "a", "c"})),
		"a list picks and orders, dropping unknown and repeated names")
}

func TestServesComparesNormalisedLanguageTags(t *testing.T) {
	assert.True(t, providerset.Entry{}.Serves([]string{"fr"}), "no restriction serves everything")
	e := providerset.Entry{Languages: []string{"pt_BR", "eng"}}
	assert.True(t, e.Serves([]string{"pt-BR"}))
	assert.True(t, e.Serves([]string{"en"}), "ISO 639-2 eng normalises to en")
	assert.False(t, e.Serves([]string{"pt"}), "pt and pt-BR are distinct")
}
