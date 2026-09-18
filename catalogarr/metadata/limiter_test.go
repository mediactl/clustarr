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

package metadata

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

func TestResolveLimiterUsesThePackageFloorByDefault(t *testing.T) {
	l := resolveLimiter(catalogv1alpha1.MetadataProviderTMDB, nil)
	d := pkgmetadata.DefaultLimits()
	require.Equal(t, d.TMDB, l.Limit())
	require.Equal(t, d.TMDBBurst, l.Burst())
}

func TestResolveLimiterHonoursTheCRDOverride(t *testing.T) {
	q := resource.MustParse("2.5")
	l := resolveLimiter(catalogv1alpha1.MetadataProviderTMDB, &catalogv1alpha1.RateLimit{
		RequestsPerSecond: &q,
		Burst:             10,
	})
	require.InDelta(t, 2.5, float64(l.Limit()), 0.001)
	require.Equal(t, 10, l.Burst())
}

func TestResolveLimiterFallsBackForAnUnimplementedType(t *testing.T) {
	l := resolveLimiter(catalogv1alpha1.MetadataProviderFanart, nil)
	require.Equal(t, 1, l.Burst())
}
