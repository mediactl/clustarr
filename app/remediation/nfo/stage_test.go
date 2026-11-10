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

package nfo_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/nfo"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The stage runs for the four kinds of design §8.1 -- the Artist and its
// Albums, the Author (as the artist of an audiobook root) and its
// Audiobooks -- and declares catalog-nfo's set on each; a Book, a Comic or
// a Movie gets no NFO file.
func TestTheStageAppliesToTheFourNFOKinds(t *testing.T) {
	s := nfo.New(nfo.Options{})
	for _, k := range []remediation.KeyKind{remediation.KindArtist, remediation.KindAlbum, remediation.KindAuthor, remediation.KindAudiobook} {
		assert.True(t, s.Applies(k), k)
		assert.Equal(t, []k8s.FieldManager{k8s.ManagerCatalogNFO}, s.DeclaresSets(k), k)
	}
	for _, k := range []remediation.KeyKind{remediation.KindMovie, remediation.KindSeries, remediation.KindEpisode,
		remediation.KindBook, remediation.KindComic, remediation.KindIssue, remediation.KindMediaFile} {
		assert.False(t, s.Applies(k), k)
		assert.Nil(t, s.DeclaresSets(k), k)
	}
}

// Only a music or audiobook root writes NFO files, and only while its
// naming.writeNFO is not false: an Author under a book root plans nothing.
func TestOnlyAMusicOrAudiobookRootWritesNFOFiles(t *testing.T) {
	root := func(kind catalogv1alpha1.RootFolderKind, write *bool) *catalogv1alpha1.RootFolder {
		return &catalogv1alpha1.RootFolder{Spec: catalogv1alpha1.RootFolderSpec{Kind: kind, Naming: catalogv1alpha1.NamingSpec{WriteNFO: write}}}
	}
	off := false
	assert.True(t, nfo.Enabled(root(catalogv1alpha1.RootFolderKindMusic, nil)))
	assert.True(t, nfo.Enabled(root(catalogv1alpha1.RootFolderKindAudiobook, nil)))
	assert.False(t, nfo.Enabled(root(catalogv1alpha1.RootFolderKindAudiobook, &off)))
	assert.False(t, nfo.Enabled(root(catalogv1alpha1.RootFolderKindBook, nil)), "an Author under a book root gets no artist.nfo")
	assert.False(t, nfo.Enabled(root(catalogv1alpha1.RootFolderKindMovie, nil)))
}
