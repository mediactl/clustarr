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
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/app/remediation/naming"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestNamingNumbersAnAudiobooksPartsAmongItsFiles is the adapter on an
// Audiobook's parts (2026-11-10 design D7, D11; C5): {PartNumber} is the
// file's place among the Audiobook's MediaFiles in play order -- a two-disc
// set's keys 1001, 1002, 2001 and 2002 (spec.partIndex, disc*1000+track)
// are proposed as parts 01 to 04, never as "(2001)" -- so the loop proposes
// the path the import placed the part at, and a one-file audiobook carries
// no part. A listing the cache cannot answer is Gather's error, so the pass
// keeps the file's previous proposal rather than number it from nothing.
func TestNamingNumbersAnAudiobooksPartsAmongItsFiles(t *testing.T) {
	meta := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: "media", Name: name} }
	const folder = "/data/audiobooks/Terry Pratchett/Discworld/8 - 1989 - Guards! Guards! Nigel Planer/8 - 1989 - Guards! Guards! Nigel Planer"
	ab := func(parts int32) *catalogv1alpha1.Audiobook {
		return &catalogv1alpha1.Audiobook{
			ObjectMeta: meta("guards"),
			Spec:       catalogv1alpha1.AudiobookSpec{ASIN: "B000", RootFolderRef: "audiobooks", QualityProfileRef: "m4b"},
			Status: catalogv1alpha1.AudiobookStatus{PartCount: parts, Metadata: &catalogv1alpha1.AudiobookMetadata{
				Title:       "Guards! Guards!",
				Authors:     []catalogv1alpha1.NamedRef{{Name: "Terry Pratchett"}},
				Narrators:   []string{"Nigel Planer"},
				Series:      &catalogv1alpha1.SeriesLink{Series: "Discworld", Position: "8"},
				ReleaseDate: &metav1.Time{Time: time.Date(1989, time.August, 1, 0, 0, 0, 0, time.UTC)},
			}},
		}
	}
	root := &catalogv1alpha1.RootFolder{ObjectMeta: meta("audiobooks"), Spec: catalogv1alpha1.RootFolderSpec{Path: "/data/audiobooks", Kind: catalogv1alpha1.RootFolderKindAudiobook}}
	ref := commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards"}
	part := func(name, path string, key int32) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{ObjectMeta: meta(name), Spec: catalogv1alpha1.MediaFileSpec{MediaRef: ref, Path: path, PartIndex: key}}
	}
	indexed := func(objs ...client.Object) client.Client {
		return fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).
			WithIndex(&catalogv1alpha1.MediaFile{}, mfindex.Item, mfindex.Extract).Build()
	}
	name := func(t *testing.T, r client.Reader, mf *catalogv1alpha1.MediaFile) *catalogv1alpha1.NamingStatus {
		t.Helper()
		a := naming.New(naming.Options{})
		require.True(t, a.Applies(mf))
		view := &remediation.View{File: mf, Prev: &catalogv1alpha1.MediaFileStatus{}, Draft: &catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{Container: "mp3"}}, Now: metav1.Now()}
		in, err := a.Gather(t.Context(), &remediation.Env{Reader: r}, view)
		require.NoError(t, err)
		var out catalogv1alpha1.MediaFileStatus
		_, err = a.Plan(view, in, &out)
		require.NoError(t, err)
		require.NotNil(t, out.Naming)
		return out.Naming
	}

	t.Run("two discs", func(t *testing.T) {
		// Placed by the import as parts 01..04 (fileimport's placeParts);
		// the fourth sits at its download name, as a rescan would find it.
		files := []*catalogv1alpha1.MediaFile{
			part("d1c1", folder+" (01).mp3", 1001),
			part("d1c2", folder+" (02).mp3", 1002),
			part("d2c1", folder+" (03).mp3", 2001),
			part("d2c2", "/data/audiobooks/Pratchett/CD2/Chapter 04.mp3", 2002),
		}
		r := indexed(root, ab(4), files[0], files[1], files[2], files[3])
		for i, mf := range files[:3] {
			n := name(t, r, mf)
			assert.Empty(t, n.Reason, mf.Name)
			assert.Equal(t, mf.Spec.Path, n.ExpectedPath, "%s is part %d, not part %d", mf.Name, i+1, mf.Spec.PartIndex)
			assert.True(t, n.Current, mf.Name)
		}
		n := name(t, r, files[3])
		assert.Equal(t, folder+" (04).mp3", n.ExpectedPath, "disc 2 track 2 is the fourth part")
		assert.False(t, n.Current)
	})

	t.Run("a lone part has no number", func(t *testing.T) {
		mf := part("whole", "/data/audiobooks/Pratchett/whole.m4b", 1003)
		n := name(t, indexed(root, ab(1), mf), mf)
		assert.Equal(t, folder+".m4b", n.ExpectedPath)
	})

	t.Run("a file the cache has yet to list counts itself", func(t *testing.T) {
		listed := part("d1c1", folder+" (01).mp3", 1001)
		fresh := part("d1c2", "/data/audiobooks/Pratchett/Chapter 02.mp3", 1002)
		n := name(t, indexed(root, ab(2), listed), fresh)
		assert.Equal(t, folder+" (02).mp3", n.ExpectedPath, "the second of two, its sibling listed and itself not yet")
	})

	t.Run("a listing that fails is Gather's error", func(t *testing.T) {
		mf := part("d1c1", folder+" (01).mp3", 1001)
		a := naming.New(naming.Options{})
		view := &remediation.View{File: mf, Prev: &catalogv1alpha1.MediaFileStatus{}, Draft: &catalogv1alpha1.MediaFileStatus{}}
		_, err := a.Gather(t.Context(), &remediation.Env{Reader: failingList{Reader: indexed(root, ab(2), mf)}}, view)
		require.Error(t, err, "the pass keeps the previous proposal over a listing it cannot read")
	})
}

// failingList is a reader whose every List fails, as a cache blip would.
type failingList struct{ client.Reader }

func (failingList) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("the cache is not synced")
}
