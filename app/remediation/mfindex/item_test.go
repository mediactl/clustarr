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

package mfindex_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func file(ns, name string, ref commonv1.MediaRef) *catalogv1alpha1.MediaFile {
	return &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: ref, Path: "/data/media/" + name + ".mkv"},
	}
}

// builderIndexer registers Register's indexes on a fake client builder.
type builderIndexer struct{ b *fake.ClientBuilder }

func (i builderIndexer) IndexField(_ context.Context, obj client.Object, field string, fn client.IndexerFunc) error {
	i.b.WithIndex(obj, field, fn)
	return nil
}

// TestExtractNamesTheItemAndEveryKey: one index serves every item kind and
// every item a pack covers -- the second episode of a multi-episode file
// finds it, which caption's index never did (loop spec §3.16).
func TestExtractNamesTheItemAndEveryKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  commonv1.MediaRef
		want []string
	}{
		{"movie", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, []string{"movie/heat"}},
		{"multi-episode file", commonv1.MediaRef{
			Kind: commonv1.MediaKindEpisode, Name: "show-s01e01", Keys: []string{"show-s01e01", "show-s01e02"},
		}, []string{"episode/show-s01e01", "episode/show-s01e02"}},
		{"a track narrows the album and adds no value", commonv1.MediaRef{
			Kind: commonv1.MediaKindAlbum, Name: "ok-computer", Track: "rec-2",
		}, []string{"album/ok-computer"}},
		{"book", commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "earthsea"}, []string{"book/earthsea"}},
		{"audiobook", commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-guards"}, []string{"audiobook/guards-guards"}},
		{"issue pack, an empty key skipped", commonv1.MediaRef{
			Kind: commonv1.MediaKindIssue, Name: "saga-1", Keys: []string{"saga-1", "saga-2", ""},
		}, []string{"issue/saga-1", "issue/saga-2"}},
		{"a kind without a name", commonv1.MediaRef{Kind: commonv1.MediaKindMovie}, nil},
		{"attributed to nothing", commonv1.MediaRef{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, mfindex.Extract(file("media", "f", tc.ref)))
		})
	}
	assert.Nil(t, mfindex.Extract(&catalogv1alpha1.Movie{}), "another kind indexes nothing")
}

// TestItemKey: the value is the one librarydelete and the item packages
// build, "<kind>/<name>" in the MediaRef vocabulary.
func TestItemKey(t *testing.T) {
	assert.Equal(t, "episode/show-s01e02", mfindex.ItemKey(commonv1.MediaKindEpisode, "show-s01e02"))
	assert.Equal(t, "movie/heat", mfindex.ItemKey(commonv1.MediaKindMovie, "heat"))
}

// TestIndexDeclaresTheOneName: Index is the one declaration of Item, with
// Extract as its extractor.
func TestIndexDeclaresTheOneName(t *testing.T) {
	assert.Equal(t, "remediation.mediafile.item", mfindex.Item)
	assert.Equal(t, mfindex.Item, mfindex.Index.Name)
	assert.IsType(t, &catalogv1alpha1.MediaFile{}, mfindex.Index.Object)
	require.NotNil(t, mfindex.Index.Extract)
	f := file("media", "f", commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "e1", Keys: []string{"e1", "e2"}})
	assert.Equal(t, mfindex.Extract(f), mfindex.Index.Extract(f))
}

// TestFilesOfListsThroughTheIndex: FilesOf lists through the index, never
// the namespace, and narrows by kind and namespace both.
func TestFilesOfListsThroughTheIndex(t *testing.T) {
	b := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
		file("media", "heat-1080p", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}),
		file("media", "s01e01e02", commonv1.MediaRef{
			Kind: commonv1.MediaKindEpisode, Name: "show-s01e01", Keys: []string{"show-s01e01", "show-s01e02"},
		}),
		file("other", "heat-elsewhere", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}),
		file("media", "an-episode-named-heat", commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "heat"}),
	).WithInterceptorFuncs(interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		lo := (&client.ListOptions{}).ApplyOptions(opts)
		require.False(t, lo.FieldSelector == nil || lo.FieldSelector.Empty(), "FilesOf listed the whole namespace")
		return c.List(ctx, list, opts...)
	}})
	require.NoError(t, mfindex.Register(context.Background(), builderIndexer{b}))
	c := b.Build()

	names := func(kind commonv1.MediaKind, name string) []string {
		files, err := mfindex.FilesOf(context.Background(), c, "media", kind, name)
		require.NoError(t, err)
		out := []string{}
		for _, f := range files {
			out = append(out, f.Name)
		}
		return out
	}
	assert.Equal(t, []string{"heat-1080p"}, names(commonv1.MediaKindMovie, "heat"))
	assert.Equal(t, []string{"s01e01e02"}, names(commonv1.MediaKindEpisode, "show-s01e01"))
	assert.Equal(t, []string{"s01e01e02"}, names(commonv1.MediaKindEpisode, "show-s01e02"), "the second episode finds the pack")
	assert.Equal(t, []string{"an-episode-named-heat"}, names(commonv1.MediaKindEpisode, "heat"), "the kind narrows")
	assert.Equal(t, []string{}, names(commonv1.MediaKindBook, "heat"))
}
