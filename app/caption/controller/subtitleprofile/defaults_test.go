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

package subtitleprofile_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/controller/subtitleprofile"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A cluster with no subtitle profile gets an English default, so every
// movie and episode wants English subtitles without any setup.
func TestSeedDefaultCreatesAnEnglishDefaultProfile(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	require.NoError(t, subtitleprofile.SeedDefault(ctx, c, "media"))
	require.NoError(t, subtitleprofile.SeedDefault(ctx, c, "media"), "a second start is a no-op")

	var l subtitlev1alpha1.SubtitleProfileList
	require.NoError(t, c.List(ctx, &l))
	require.Len(t, l.Items, 1)
	p := l.Items[0]
	require.Equal(t, "media", p.Namespace)
	require.Equal(t, "english", p.Name)
	require.True(t, p.Spec.Default)
	require.Nil(t, p.Spec.Selector)
	require.Equal(t, []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en", AudioExclude: true}}, p.Spec.Languages,
		"English is wanted only where the audio is not already English")
}

// A cluster where the owner has any subtitle profile, in any namespace,
// default or not, gets none: a second default would be Invalid, and a
// selector-only setup is theirs to extend.
func TestSeedDefaultLeavesAConfiguredClusterAlone(t *testing.T) {
	ctx := context.Background()
	mine := &subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Namespace: "elsewhere", Name: "anime"},
		Spec: subtitlev1alpha1.SubtitleProfileSpec{
			Selector:  &metav1.LabelSelector{MatchLabels: map[string]string{"anime": "true"}},
			Languages: []subtitlev1alpha1.LanguageItem{{Key: "ja", Language: "ja"}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(mine).Build()
	require.NoError(t, subtitleprofile.SeedDefault(ctx, c, "media"))

	var l subtitlev1alpha1.SubtitleProfileList
	require.NoError(t, c.List(ctx, &l, client.InNamespace("media")))
	require.Empty(t, l.Items)
}

// With no namespace (`clustarr all` from a shell) nothing is created.
func TestSeedDefaultWithoutANamespaceCreatesNothing(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	require.NoError(t, subtitleprofile.SeedDefault(ctx, c, ""))
	var l subtitlev1alpha1.SubtitleProfileList
	require.NoError(t, c.List(ctx, &l))
	require.Empty(t, l.Items)
}
