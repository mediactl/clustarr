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
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// An audnexus MetadataProvider wires one client into both the Audiobooks
// and the AudiobookAuthors slots (2026-11-10 design D11; task L2): the
// author lookup Add New's author link calls (plan W4c) is the same
// keyless Audnexus instance, under the same limiter.
func TestBuildRegistryWiresAudnexusAsTheAudiobookAuthorProviderToo(t *testing.T) {
	providers := []catalogv1alpha1.MetadataProvider{{
		ObjectMeta: metav1.ObjectMeta{Name: "audnexus", Namespace: "clustarr"},
		Spec:       catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderAudnexus, Enabled: enabled(), Priority: 50},
	}}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()

	reg, err := BuildRegistry(context.Background(), c, providers, http.DefaultClient)
	require.NoError(t, err)

	require.Len(t, reg.Audiobooks, 1)
	require.Len(t, reg.AudiobookAuthors, 1)
	require.Equal(t, "audnexus", reg.AudiobookAuthors[0].Name())
	require.Same(t, reg.Audiobooks[0], reg.AudiobookAuthors[0], "the one client, not a second instance with its own limiter")
}
