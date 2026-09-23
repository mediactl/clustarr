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

package rollup_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

func TestItemAction(t *testing.T) {
	assert.Equal(t, events.ActionAdded, rollup.ItemAction(1, 0, false), "the first reconcile announces the item")
	assert.Equal(t, events.ActionAdded, rollup.ItemAction(2, 1, false), "addOptionsApplied, not generation, decides added")
	assert.Equal(t, "", rollup.ItemAction(1, 1, true), "a settled item announces nothing")
	assert.Equal(t, events.ActionUpdated, rollup.ItemAction(3, 2, true), "a new spec generation is an update")
	assert.Equal(t, "", rollup.ItemAction(3, 0, true), "no recorded generation is not evidence of an edit")
}

func TestFileTransition(t *testing.T) {
	mf := func(name string) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}
	ref := func(s string) *string { return &s }
	cases := []struct {
		name       string
		old        *string
		now        *catalogv1alpha1.MediaFile
		wantAction string
		wantFile   string
	}{
		{"no file before or after", nil, nil, "", ""},
		{"first file", nil, mf("a"), events.ActionImported, "a"},
		{"same file", ref("a"), mf("a"), "", ""},
		{"a different file took over", ref("a"), mf("b"), events.ActionReplaced, "b"},
		{"the file is gone", ref("a"), nil, events.ActionDeleted, "a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			action, file := rollup.FileTransition(c.old, c.now)
			assert.Equal(t, c.wantAction, action)
			assert.Equal(t, c.wantFile, file)
		})
	}
}

func TestItemEventEnvelope(t *testing.T) {
	at := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	m := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "media", UID: "u-1", Generation: 4}}

	subject, env, err := rollup.ItemEvent(m, commonv1.MediaKindMovie, events.ActionUpdated,
		rollup.ItemFields{Title: "Heat", Year: 1995, IDs: map[string]string{"tmdb": "949"}, Monitored: true}, at)
	require.NoError(t, err)
	assert.Equal(t, "clustarr.evt.catalog.movie.updated.u-1", subject)
	assert.Equal(t, "u-1:updated:4", env.ID, "an update is once per generation")
	assert.Equal(t, "media/heat", env.Key)

	var p schema.ItemEvent
	require.NoError(t, schema.Decode(env.Schema, env.Data, &p))
	assert.Equal(t, "heat", p.Media.Name)
	assert.Equal(t, commonv1.MediaKindMovie, p.Media.Kind)
	assert.Equal(t, "u-1", p.Ref.UID)
	assert.Equal(t, "Heat", p.Title)
	assert.EqualValues(t, 1995, p.Year)
	assert.True(t, p.Monitored)

	_, env, err = rollup.ItemEvent(m, commonv1.MediaKindMovie, events.ActionAdded, rollup.ItemFields{}, at)
	require.NoError(t, err)
	assert.Equal(t, "u-1:added", env.ID, "an item is added once")
}

func TestMediaFileEventEnvelope(t *testing.T) {
	at := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	m := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "media", UID: "u-1"}}
	media := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}
	file := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "heat-abc", Namespace: "media"},
		Spec: catalogv1alpha1.MediaFileSpec{
			Path: "/data/media/movies/Heat (1995)/Heat.mkv", SizeBytes: 42,
			Quality:      commonv1.Quality{Name: "Bluray-1080p"},
			ImportedFrom: &catalogv1alpha1.ImportSource{DownloadRef: "heat-dl", Manual: true},
		},
	}

	subject, env, err := rollup.MediaFileEvent(m, media, events.ActionImported, "heat-abc", file, at)
	require.NoError(t, err)
	assert.Equal(t, "clustarr.evt.catalog.mediafile.imported.u-1", subject, "the <uid> is the item's")
	assert.Equal(t, "u-1:mediafile:imported:heat-abc", env.ID)
	var p schema.MediaFileEvent
	require.NoError(t, schema.Decode(env.Schema, env.Data, &p))
	assert.Equal(t, file.Spec.Path, p.ImportedPath)
	assert.EqualValues(t, 42, p.SizeBytes)
	assert.Equal(t, schema.MediaFileReasonManual, p.Reason)
	require.NotNil(t, p.DownloadRef)
	assert.Equal(t, "heat-dl", p.DownloadRef.Name)

	_, env, err = rollup.MediaFileEvent(m, media, events.ActionReplaced, "heat-abc", file, at)
	require.NoError(t, err)
	require.NoError(t, schema.Decode(env.Schema, env.Data, &p))
	assert.Equal(t, schema.MediaFileReasonUpgrade, p.Reason, "a replacement is an upgrade, even when hand-imported")

	_, env, err = rollup.MediaFileEvent(m, media, events.ActionDeleted, "heat-old", nil, at)
	require.NoError(t, err)
	p = schema.MediaFileEvent{}
	require.NoError(t, schema.Decode(env.Schema, env.Data, &p))
	assert.Empty(t, p.Reason, "nothing knows why a file went; the reason is not guessed")
	assert.Equal(t, "u-1:mediafile:deleted:heat-old", env.ID)
}

func TestTransitioned(t *testing.T) {
	prev := []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: "RootFolderNotFound"}}
	assert.False(t, rollup.Transitioned(prev, "Ready", metav1.ConditionFalse, "RootFolderNotFound"), "a steady state is not an edge")
	assert.True(t, rollup.Transitioned(prev, "Ready", metav1.ConditionFalse, "Other"), "a new reason is an edge")
	assert.True(t, rollup.Transitioned(prev, "Ready", metav1.ConditionTrue, "RootFolderNotFound"), "a new status is an edge")
	assert.True(t, rollup.Transitioned(prev, "QueueFull", metav1.ConditionTrue, "QueueFull"), "an absent condition is an edge")
}
