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

package plexfilter_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/plexfilter"
)

// The PlexCollection samples decode strictly into the API, pass the rules
// the UI checks before a write, and read as the design's three reference
// collections (config/samples/README.md: a sample is a claim about what
// its controller does).
func TestThePlexCollectionSamplesAreTheReferences(t *testing.T) {
	for file, want := range map[string]catalogv1.PlexCollectionSpec{
		"catalog_v1alpha1_plexcollection_anime_dubbed.yaml": animeDubbed(),
		"catalog_v1alpha1_plexcollection_netflix.yaml":      netflix(),
		"catalog_v1alpha1_plexcollection_up.yaml":           up(),
	} {
		raw, err := os.ReadFile("../../config/samples/" + file)
		require.NoError(t, err, file)
		var pc catalogv1.PlexCollection
		require.NoError(t, yaml.UnmarshalStrict(raw, &pc), file)
		assert.Equal(t, "PlexCollection", pc.Kind, file)
		assert.Equal(t, "clustarr-system", pc.Namespace, file)
		assert.Equal(t, want, pc.Spec, file)
		assert.NoError(t, plexfilter.Validate(pc.Spec), file)
	}
}
