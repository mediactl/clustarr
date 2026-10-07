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

package build

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/providerset"
)

// TestCapabilityTableMatchesTheClients keeps §13 OD9's promise: providerset's
// static table says, for every spec.type the CRD admits, exactly what a
// zero-config client of the type says about itself -- the guarantee
// providerset used to get by building one.
func TestCapabilityTableMatchesTheClients(t *testing.T) {
	for _, typ := range providerTypes(t) {
		t.Run(string(typ), func(t *testing.T) {
			c := prototype(typ)
			if c == nil {
				assert.Empty(t, providerset.NeedsSecrets(typ), "a type with no client needs no Secret keys")
				assert.False(t, providerset.HIVerifiable(typ), "a type with no client vouches for nothing")
				return
			}
			assert.Equal(t, c.Capabilities().NeedsSecrets, providerset.NeedsSecrets(typ))
			assert.Equal(t, c.HIVerifiable(), providerset.HIVerifiable(typ))
		})
	}
}

// providerTypes is spec.type's enum as the generated CRD installs it, so a
// type added to the API cannot skip this check.
func providerTypes(t *testing.T) []subtitlev1alpha1.SubtitleProviderType {
	t.Helper()
	raw, err := os.ReadFile("../../../../config/crd/bases/subtitle.clustarr.io_subtitleproviders.yaml")
	require.NoError(t, err)
	var crd struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Spec struct {
								Properties map[string]struct {
									Enum []string `json:"enum"`
								} `json:"properties"`
							} `json:"spec"`
						} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	require.NotEmpty(t, crd.Spec.Versions, "the CRD was not parsed; run `make manifests`")
	enum := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Spec.Properties["type"].Enum
	require.NotEmpty(t, enum, "spec.type lost its enum")
	out := make([]subtitlev1alpha1.SubtitleProviderType, 0, len(enum))
	for _, e := range enum {
		out = append(out, subtitlev1alpha1.SubtitleProviderType(e))
	}
	return out
}
