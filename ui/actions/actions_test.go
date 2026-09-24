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

package actions_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/ui/actions"
)

// fakeWriter records every call and returns err from each.
type fakeWriter struct {
	err       error
	createErr error // returned by Create alone, when set
	creates   []createCall
	patches   []patchCall
	deletes   []client.Object
}

type createCall struct {
	obj  client.Object
	opts client.CreateOptions
}

type patchCall struct {
	obj       client.Object
	patchType types.PatchType
	data      []byte
	opts      client.PatchOptions
}

func (f *fakeWriter) Create(_ context.Context, obj client.Object, opts ...client.CreateOption) error {
	var o client.CreateOptions
	o.ApplyOptions(opts)
	f.creates = append(f.creates, createCall{obj: obj, opts: o})
	if f.createErr != nil {
		return f.createErr
	}
	return f.err
}

func (f *fakeWriter) Delete(_ context.Context, obj client.Object, _ ...client.DeleteOption) error {
	f.deletes = append(f.deletes, obj)
	return f.err
}

func (f *fakeWriter) Patch(_ context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	data, err := patch.Data(obj)
	if err != nil {
		return err
	}
	var o client.PatchOptions
	o.ApplyOptions(opts)
	f.patches = append(f.patches, patchCall{obj: obj, patchType: patch.Type(), data: data, opts: o})
	return f.err
}

// TestFieldManagerIsK8sManagerUI pins ui/actions' restated field manager to
// pkg/k8s.ManagerUI. ui/ may not import pkg/k8s (ui/guard_test.go), so this
// is what stops the two strings drifting apart.
func TestFieldManagerIsK8sManagerUI(t *testing.T) {
	require.Equal(t, k8s.ManagerUI.String(), actions.FieldManager)
	require.True(t, k8s.ManagerUI.Valid())
}

func TestSetMonitoredSendsOneMergePatchOfOneLeaf(t *testing.T) {
	for _, monitored := range []bool{false, true} {
		w := &fakeWriter{}
		obj, err := actions.SetMonitored(t.Context(), w, "media", commonv1.MediaKindEpisode, "breaking-bad-s01e01", monitored)
		require.NoError(t, err)
		require.Len(t, w.patches, 1)
		require.Empty(t, w.creates)

		p := w.patches[0]
		require.Same(t, obj, p.obj, "SetMonitored must return the object the patch response was decoded into")
		require.IsType(t, &catalogv1alpha1.Episode{}, p.obj, "an episode patch must go to Episode, not another kind")
		require.Equal(t, "media", p.obj.GetNamespace())
		require.Equal(t, "breaking-bad-s01e01", p.obj.GetName())
		require.Equal(t, types.MergePatchType, p.patchType,
			"spec.monitored is a JSON merge patch, not an apply -- see ui/actions' package doc")
		if monitored {
			require.JSONEq(t, `{"spec":{"monitored":true}}`, string(p.data))
		} else {
			require.JSONEq(t, `{"spec":{"monitored":false}}`, string(p.data),
				"false must be sent, not omitted: it is the value the user asked for")
		}
		require.Equal(t, actions.FieldManager, p.opts.FieldManager)
		require.Nil(t, p.opts.Force, "a merge patch takes no force option")
	}
}

// TestSetMonitoredRoutesEveryKindToItsOwnType covers the kind -> object
// table: a wrong entry would patch the wrong resource (or a different
// item's same-named object of another kind).
func TestSetMonitoredRoutesEveryKindToItsOwnType(t *testing.T) {
	want := map[commonv1.MediaKind]client.Object{
		commonv1.MediaKindMovie:     &catalogv1alpha1.Movie{},
		commonv1.MediaKindSeries:    &catalogv1alpha1.Series{},
		commonv1.MediaKindEpisode:   &catalogv1alpha1.Episode{},
		commonv1.MediaKindArtist:    &catalogv1alpha1.Artist{},
		commonv1.MediaKindAlbum:     &catalogv1alpha1.Album{},
		commonv1.MediaKindAuthor:    &catalogv1alpha1.Author{},
		commonv1.MediaKindBook:      &catalogv1alpha1.Book{},
		commonv1.MediaKindAudiobook: &catalogv1alpha1.Audiobook{},
		commonv1.MediaKindComic:     &catalogv1alpha1.Comic{},
		commonv1.MediaKindIssue:     &catalogv1alpha1.Issue{},
	}
	require.Len(t, actions.MediaKinds(), len(want), "every commonv1.MediaKind is monitorable, and nothing else")
	for _, kind := range actions.MediaKinds() {
		w := &fakeWriter{}
		_, err := actions.SetMonitored(t.Context(), w, "media", kind, "x", true)
		require.NoError(t, err, kind)
		require.Len(t, w.patches, 1, kind)
		require.IsType(t, want[kind], w.patches[0].obj, kind)
	}
}

func TestSearchNowCreatesALabelledSearchForTheItem(t *testing.T) {
	w := &fakeWriter{}
	s, err := actions.SearchNow(t.Context(), w, "media", commonv1.MediaKindMovie, "the-matrix-1999")
	require.NoError(t, err)
	require.Len(t, w.creates, 1)
	require.Empty(t, w.patches)
	require.Same(t, s, w.creates[0].obj)

	require.Empty(t, s.Name, "the apiserver names it")
	require.Equal(t, "the-matrix-1999-", s.GenerateName)
	require.Equal(t, "media", s.Namespace)
	require.Equal(t, map[string]string{actions.LabelOrigin: actions.OriginUI}, s.Labels)
	require.Equal(t, catalogv1alpha1.SearchSpec{
		MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix-1999"},
	}, s.Spec, "everything but mediaRef is left to the CRD's defaults")
	require.Equal(t, catalogv1alpha1.SearchStatus{}, s.Status)
	require.Equal(t, actions.FieldManager, w.creates[0].opts.FieldManager)
}

func TestRescanCreatesALabelledLibraryScan(t *testing.T) {
	w := &fakeWriter{}
	scan, err := actions.Rescan(t.Context(), w, "media", "movies")
	require.NoError(t, err)
	require.Len(t, w.creates, 1)
	require.Empty(t, w.patches)
	require.Same(t, scan, w.creates[0].obj)

	require.Empty(t, scan.Name)
	require.Equal(t, "movies-", scan.GenerateName)
	require.Equal(t, "media", scan.Namespace)
	require.Equal(t, map[string]string{actions.LabelOrigin: actions.OriginUI}, scan.Labels,
		"only the origin label -- not the RootFolder schedule's own, which it uses to find its scans")
	require.Equal(t, catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies"}, scan.Spec)
	require.Equal(t, catalogv1alpha1.LibraryScanStatus{}, scan.Status)
	require.Equal(t, actions.FieldManager, w.creates[0].opts.FieldManager)
}

// TestRescanPathRestrictsTheScanToAFolder: Radarr's "Refresh & Scan" on
// an item rescans its own folder, so RescanPath creates the same
// LibraryScan as Rescan with Subpath set to the folder under the
// RootFolder; an empty subpath is Rescan itself, and a subpath that
// escapes the root is refused without writing.
func TestRescanPathRestrictsTheScanToAFolder(t *testing.T) {
	w := &fakeWriter{}
	scan, err := actions.RescanPath(t.Context(), w, "media", "movies", "Nerve (2016) {tmdb-328387}")
	require.NoError(t, err)
	require.Len(t, w.creates, 1)
	require.Equal(t, "movies-", scan.GenerateName)
	require.Equal(t, catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies", Subpath: "Nerve (2016) {tmdb-328387}"}, scan.Spec)
	require.Equal(t, map[string]string{actions.LabelOrigin: actions.OriginUI}, scan.Labels)

	w = &fakeWriter{}
	scan, err = actions.RescanPath(t.Context(), w, "media", "movies", "")
	require.NoError(t, err)
	require.Equal(t, catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies"}, scan.Spec, "no subpath is a whole-root rescan")

	for _, bad := range []string{"../elsewhere", "/abs", "a/../../b"} {
		w = &fakeWriter{}
		_, err = actions.RescanPath(t.Context(), w, "media", "movies", bad)
		require.ErrorIs(t, err, actions.ErrInvalid, bad)
		require.Empty(t, w.creates, bad)
	}
}

func TestActionsRejectInvalidInputWithoutWriting(t *testing.T) {
	cases := map[string]func(*fakeWriter) error{
		"search: no namespace": func(w *fakeWriter) error {
			_, err := actions.SearchNow(t.Context(), w, "", commonv1.MediaKindMovie, "x")
			return err
		},
		"search: no name": func(w *fakeWriter) error {
			_, err := actions.SearchNow(t.Context(), w, "media", commonv1.MediaKindMovie, "")
			return err
		},
		"search: unknown kind": func(w *fakeWriter) error {
			_, err := actions.SearchNow(t.Context(), w, "media", "podcast", "x")
			return err
		},
		"rescan: no namespace": func(w *fakeWriter) error {
			_, err := actions.Rescan(t.Context(), w, "", "movies")
			return err
		},
		"rescan: no root folder": func(w *fakeWriter) error {
			_, err := actions.Rescan(t.Context(), w, "media", "")
			return err
		},
		"monitored: no namespace": func(w *fakeWriter) error {
			_, err := actions.SetMonitored(t.Context(), w, "", commonv1.MediaKindMovie, "x", true)
			return err
		},
		"monitored: no name": func(w *fakeWriter) error {
			_, err := actions.SetMonitored(t.Context(), w, "media", commonv1.MediaKindMovie, "", true)
			return err
		},
		"monitored: unknown kind": func(w *fakeWriter) error {
			_, err := actions.SetMonitored(t.Context(), w, "media", "podcast", "x", true)
			return err
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			w := &fakeWriter{}
			require.ErrorIs(t, run(w), actions.ErrInvalid)
			require.Empty(t, w.creates)
			require.Empty(t, w.patches)
		})
	}
}

func TestActionsWrapTheAPIServersError(t *testing.T) {
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: "catalog.clustarr.io", Resource: "movies"}, "gone")
	w := &fakeWriter{err: notFound}

	_, err := actions.SetMonitored(t.Context(), w, "media", commonv1.MediaKindMovie, "gone", false)
	require.True(t, apierrors.IsNotFound(err), "a deleted item must read as NotFound to the handler: %v", err)
	require.ErrorIs(t, err, notFound)

	_, err = actions.SearchNow(t.Context(), w, "media", commonv1.MediaKindMovie, "gone")
	require.ErrorIs(t, err, notFound)

	_, err = actions.Rescan(t.Context(), w, "media", "movies")
	require.ErrorIs(t, err, notFound)
}

func TestActionsWithNoWriterRefuse(t *testing.T) {
	for name, a := range map[string]*actions.Actions{"nil *Actions": nil, "nil Writer": actions.New(nil)} {
		t.Run(name, func(t *testing.T) {
			_, err := a.SearchNow(t.Context(), "media", commonv1.MediaKindMovie, "x")
			require.ErrorIs(t, err, actions.ErrNoWriter)
			_, err = a.Rescan(t.Context(), "media", "movies")
			require.ErrorIs(t, err, actions.ErrNoWriter)
			_, err = a.SetMonitored(t.Context(), "media", commonv1.MediaKindMovie, "x", true)
			require.ErrorIs(t, err, actions.ErrNoWriter)
		})
	}
}

func TestActionsMethodsUseTheirWriter(t *testing.T) {
	w := &fakeWriter{}
	a := actions.New(w)
	_, err := a.SearchNow(t.Context(), "media", commonv1.MediaKindMovie, "x")
	require.NoError(t, err)
	_, err = a.Rescan(t.Context(), "media", "movies")
	require.NoError(t, err)
	_, err = a.SetMonitored(t.Context(), "media", commonv1.MediaKindMovie, "x", false)
	require.NoError(t, err)
	require.Len(t, w.creates, 2)
	require.Len(t, w.patches, 1)
}
