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
package audiobook_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/audiobook"
	"github.com/mediactl/clustarr/pkg/events"
)

func TestFileTransitions(t *testing.T) {
	part := func(name string) catalogv1alpha1.MediaFile {
		return mfPart(name, "/data/media/audiobooks/Book/"+name+".mp3")
	}
	imported := func(name string, present []catalogv1alpha1.MediaFile) audiobook.FileEdge {
		for i := range present {
			if present[i].Name == name {
				return audiobook.FileEdge{Action: events.ActionImported, File: name, MediaFile: &present[i]}
			}
		}
		t.Fatalf("fixture: %s is not present", name)
		return audiobook.FileEdge{}
	}
	deleted := func(name string) audiobook.FileEdge {
		return audiobook.FileEdge{Action: events.ActionDeleted, File: name}
	}

	t.Run("nothing recorded, nothing now", func(t *testing.T) {
		assert.Empty(t, audiobook.FileTransitions(nil, nil, nil))
	})
	t.Run("the first parts are each imported, in play order", func(t *testing.T) {
		present := []catalogv1alpha1.MediaFile{part("p2"), part("p1")}
		assert.Equal(t, []audiobook.FileEdge{imported("p1", present), imported("p2", present)},
			audiobook.FileTransitions(nil, []string{"p1", "p2"}, present))
	})
	t.Run("a settled set announces nothing", func(t *testing.T) {
		present := []catalogv1alpha1.MediaFile{part("p1"), part("p2")}
		assert.Empty(t, audiobook.FileTransitions([]string{"p1", "p2"}, []string{"p1", "p2"}, present))
	})
	t.Run("a new release replacing the old one: each new part imported, each old one deleted, no guessed pairing", func(t *testing.T) {
		present := []catalogv1alpha1.MediaFile{part("n1"), part("n2")}
		assert.Equal(t, []audiobook.FileEdge{imported("n1", present), imported("n2", present), deleted("o1"), deleted("o2")},
			audiobook.FileTransitions([]string{"o1", "o2"}, []string{"n1", "n2"}, present))
	})
	t.Run("the last parts go: each deleted", func(t *testing.T) {
		assert.Equal(t, []audiobook.FileEdge{deleted("p1"), deleted("p2")},
			audiobook.FileTransitions([]string{"p1", "p2"}, nil, nil))
	})
	t.Run("a recorded part that left the capped list but still exists was not deleted", func(t *testing.T) {
		present := []catalogv1alpha1.MediaFile{part("p1"), part("p2")}
		assert.Empty(t, audiobook.FileTransitions([]string{"p1", "p2"}, []string{"p1"}, present))
	})
}
