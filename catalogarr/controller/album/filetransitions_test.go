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
package album_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/album"
	"github.com/mediactl/clustarr/pkg/events"
)

func TestTrackFileTransitions(t *testing.T) {
	file := func(name, rec string) catalogv1alpha1.MediaFile {
		return catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "ok-computer", Track: rec}},
		}
	}
	recorded := func(rec string, ref *string) catalogv1alpha1.Track {
		return catalogv1alpha1.Track{RecordingID: rec, FileRef: ref}
	}
	declared := func(rec string, ref *string) *catalogac.TrackApplyConfiguration {
		tc := catalogac.Track().WithRecordingID(rec)
		if ref != nil {
			tc = tc.WithFileRef(*ref)
		}
		return tc
	}
	edge := func(action, name string, present []catalogv1alpha1.MediaFile) album.FileEdge {
		for i := range present {
			if present[i].Name == name {
				return album.FileEdge{Action: action, File: name, MediaFile: &present[i]}
			}
		}
		return album.FileEdge{Action: action, File: name}
	}

	t.Run("a track gaining its file is imported; a track without one, or a file with no track, announces nothing", func(t *testing.T) {
		present := []catalogv1alpha1.MediaFile{file("f2", "rec-2"), file("whole", "")}
		got := album.TrackFileTransitions(
			[]catalogv1alpha1.Track{recorded("rec-1", nil), recorded("rec-2", nil)},
			[]*catalogac.TrackApplyConfiguration{declared("rec-1", nil), declared("rec-2", ptr.To("f2"))},
			present)
		assert.Equal(t, []album.FileEdge{edge(events.ActionImported, "f2", present)}, got)
	})
	t.Run("a settled listing announces nothing", func(t *testing.T) {
		present := []catalogv1alpha1.MediaFile{file("f2", "rec-2")}
		assert.Empty(t, album.TrackFileTransitions(
			[]catalogv1alpha1.Track{recorded("rec-2", ptr.To("f2"))},
			[]*catalogac.TrackApplyConfiguration{declared("rec-2", ptr.To("f2"))},
			present))
	})
	t.Run("another file taking a track over is replaced", func(t *testing.T) {
		present := []catalogv1alpha1.MediaFile{file("f2", "rec-2"), file("f2-new", "rec-2")}
		assert.Equal(t, []album.FileEdge{edge(events.ActionReplaced, "f2-new", present)}, album.TrackFileTransitions(
			[]catalogv1alpha1.Track{recorded("rec-2", ptr.To("f2"))},
			[]*catalogac.TrackApplyConfiguration{declared("rec-2", ptr.To("f2-new"))},
			present))
	})
	t.Run("a track's file that is gone is deleted", func(t *testing.T) {
		assert.Equal(t, []album.FileEdge{edge(events.ActionDeleted, "f2", nil)}, album.TrackFileTransitions(
			[]catalogv1alpha1.Track{recorded("rec-2", ptr.To("f2"))},
			[]*catalogac.TrackApplyConfiguration{declared("rec-2", nil)},
			nil))
	})
	t.Run("a new release dropping a recording whose file is gone: deleted", func(t *testing.T) {
		assert.Equal(t, []album.FileEdge{edge(events.ActionDeleted, "f2", nil)}, album.TrackFileTransitions(
			[]catalogv1alpha1.Track{recorded("rec-2", ptr.To("f2"))},
			[]*catalogac.TrackApplyConfiguration{declared("rec-9", nil)},
			nil))
	})
	t.Run("a new release dropping a recording whose file still exists: nothing was deleted", func(t *testing.T) {
		present := []catalogv1alpha1.MediaFile{file("f2", "rec-2")}
		assert.Empty(t, album.TrackFileTransitions(
			[]catalogv1alpha1.Track{recorded("rec-2", ptr.To("f2"))},
			[]*catalogac.TrackApplyConfiguration{declared("rec-9", nil)},
			present))
	})
	t.Run("a new release listing a recording that already has a file: imported, the first time it is recorded", func(t *testing.T) {
		present := []catalogv1alpha1.MediaFile{file("f9", "rec-9")}
		assert.Equal(t, []album.FileEdge{edge(events.ActionImported, "f9", present)}, album.TrackFileTransitions(
			[]catalogv1alpha1.Track{recorded("rec-2", nil)},
			[]*catalogac.TrackApplyConfiguration{declared("rec-9", ptr.To("f9"))},
			present))
	})
}
