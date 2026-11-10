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

package naming_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/naming"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestNamingRendersAnIssueWithItsComicsSpecialVersion is the adapter on an
// Issue (2026-11-10 design D7, D10; plan W1.5's leftover): the Comic's
// spec.specialVersion reaches the {Special Version} token through
// catalogctx.Issue, so a trade paperback run's issue is proposed as
// "Saga c001 TPB.cbz" under the comic's folder, while a normal run's carries
// no label.
func TestNamingRendersAnIssueWithItsComicsSpecialVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version catalogv1alpha1.ComicSpecialVersion
		want    string
	}{
		{"a trade paperback run", catalogv1alpha1.ComicSpecialVersionTPB, "/data/comics/Saga/Saga c001 TPB.cbz"},
		{"a hard cover run", catalogv1alpha1.ComicSpecialVersionHardCover, "/data/comics/Saga/Saga c001 Hard Cover.cbz"},
		{"a normal run", catalogv1alpha1.ComicSpecialVersionNormal, "/data/comics/Saga/Saga c001.cbz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: "media", Name: name} }
			objs := []client.Object{
				&catalogv1alpha1.RootFolder{ObjectMeta: meta("comics"), Spec: catalogv1alpha1.RootFolderSpec{Path: "/data/comics", Kind: catalogv1alpha1.RootFolderKindComic}},
				&catalogv1alpha1.Comic{
					ObjectMeta: meta("saga"),
					Spec: catalogv1alpha1.ComicSpec{
						Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-1", RootFolderRef: "comics", QualityProfileRef: "cbz",
						SpecialVersion: tc.version,
					},
					Status: catalogv1alpha1.ComicStatus{Metadata: &catalogv1alpha1.ComicMetadata{Title: "Saga", Year: 2012}},
				},
				&catalogv1alpha1.Issue{ObjectMeta: meta("saga-001"), Spec: catalogv1alpha1.IssueSpec{ComicRef: "saga", Number: "001"}},
			}
			r := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).Build()
			mf := &catalogv1alpha1.MediaFile{
				ObjectMeta: meta("saga-001-cbz"),
				Spec: catalogv1alpha1.MediaFileSpec{
					MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "saga-001"},
					Path:     "/data/comics/Saga/saga 001.cbz",
				},
			}
			draft := &catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{Container: "cbz", PageCount: 160}}
			view := &remediation.View{File: mf, Prev: &catalogv1alpha1.MediaFileStatus{}, Draft: draft, Now: metav1.NewTime(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))}

			a := naming.New(naming.Options{})
			require.True(t, a.Applies(mf), "an issue's file is named")
			in, err := a.Gather(t.Context(), &remediation.Env{Reader: r}, view)
			require.NoError(t, err)
			var out catalogv1alpha1.MediaFileStatus
			_, err = a.Plan(view, in, &out)
			require.NoError(t, err)
			require.NotNil(t, out.Naming)
			require.Empty(t, out.Naming.Reason, "the issue renders")
			require.Equal(t, tc.want, out.Naming.ExpectedPath)
			require.False(t, out.Naming.Current, "the file is not at its proposed path yet")
		})
	}
}
