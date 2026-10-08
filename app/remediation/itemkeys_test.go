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

package remediation_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
)

func fileFor(ref commonv1.MediaRef) *catalogv1alpha1.MediaFile {
	return &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "f"},
		Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: ref, Path: "/data/media/f.mkv"},
	}
}

// A file is one file key and one item key per item it covers (loop spec §3.2).
func TestItemKeysAreTheItemsAFileCovers(t *testing.T) {
	key := func(kind remediation.KeyKind, name string) remediation.Key {
		return remediation.Key{Kind: kind, Namespace: "media", Name: name}
	}
	assert.Equal(t, []remediation.Key{key(remediation.KindMovie, "heat")},
		remediation.ItemKeys(fileFor(commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"})))
	assert.Equal(t, []remediation.Key{key(remediation.KindEpisode, "show-s01e01"), key(remediation.KindEpisode, "show-s01e02")},
		remediation.ItemKeys(fileFor(commonv1.MediaRef{
			Kind: commonv1.MediaKindEpisode, Name: "show-s01e01", Keys: []string{"show-s01e01", "show-s01e02"},
		})), "a multi-episode file is N Episode keys, its first episode once")
	assert.Equal(t, []remediation.Key{key(remediation.KindEpisode, "show-s01e01"), key(remediation.KindEpisode, "show-s01e02")},
		remediation.ItemKeys(fileFor(commonv1.MediaRef{
			Kind: commonv1.MediaKindEpisode, Name: "show-s01e01", Keys: []string{"", "show-s01e02", "show-s01e02"},
		})), "an empty or repeated key makes no second key")
	assert.Equal(t, []remediation.Key{key(remediation.KindAlbum, "ok-computer")},
		remediation.ItemKeys(fileFor(commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "ok-computer", Track: "rec-2"})),
		"mediaRef.track narrows an Album reference and makes no key")
	assert.Equal(t, []remediation.Key{key(remediation.KindBook, "earthsea")},
		remediation.ItemKeys(fileFor(commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "earthsea"})))
	assert.Equal(t, []remediation.Key{key(remediation.KindAudiobook, "guards-guards")},
		remediation.ItemKeys(fileFor(commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-guards"})))
	assert.Equal(t, []remediation.Key{key(remediation.KindIssue, "saga-1")},
		remediation.ItemKeys(fileFor(commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "saga-1"})))
	for _, k := range []commonv1.MediaKind{commonv1.MediaKindArtist, commonv1.MediaKindAuthor} {
		assert.Nil(t, remediation.ItemKeys(fileFor(commonv1.MediaRef{Kind: k, Name: "x"})), "%s has no item key", k)
	}
	assert.Nil(t, remediation.ItemKeys(fileFor(commonv1.MediaRef{Kind: commonv1.MediaKindMovie})), "a ref with no name")
	assert.Nil(t, remediation.ItemKeys(nil))
}

// ItemKind covers the six F4 item kinds and the two grab owners A3.2 added;
// Artist and Author hold neither files nor grabs of their own.
func TestItemKindCoversTheSixItemKinds(t *testing.T) {
	for mk, want := range map[commonv1.MediaKind]remediation.KeyKind{
		commonv1.MediaKindMovie: remediation.KindMovie, commonv1.MediaKindEpisode: remediation.KindEpisode,
		commonv1.MediaKindAlbum: remediation.KindAlbum, commonv1.MediaKindBook: remediation.KindBook,
		commonv1.MediaKindAudiobook: remediation.KindAudiobook, commonv1.MediaKindIssue: remediation.KindIssue,
		commonv1.MediaKindSeries: remediation.KindSeries, commonv1.MediaKindComic: remediation.KindComic,
	} {
		got, ok := remediation.ItemKind(mk)
		require.True(t, ok, mk)
		assert.Equal(t, want, got)
	}
	for _, mk := range []commonv1.MediaKind{commonv1.MediaKindArtist, commonv1.MediaKindAuthor, ""} {
		_, ok := remediation.ItemKind(mk)
		assert.False(t, ok, mk)
	}
	assert.Equal(t, remediation.KeyKind("Movie"), remediation.KindMovie, "§3.2's spelling")
	assert.Equal(t, remediation.KeyKind("Episode"), remediation.KindEpisode)
}
