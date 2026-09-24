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
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/ui/actions"
)

// TestAnnotationImportTargetMatchesFileimport pins actions.AnnotationImportTarget
// to fileimport.AnnotationImportTarget's value, the same way
// TestFieldManagerIsK8sManagerUI pins actions.FieldManager to
// pkg/k8s.ManagerUI's -- ui/actions cannot import importarr/worker/fileimport
// itself (ui/guard_test.go's import allowlist), so a _test.go file, which is
// exempt, is what stops the two constants drifting apart.
func TestAnnotationImportTargetMatchesFileimport(t *testing.T) {
	require.Equal(t, fileimport.AnnotationImportTarget, actions.AnnotationImportTarget)
}

// TestManualAssignTargetStringMatchesFileimportGrammar proves
// ManualAssignTarget.String() produces exactly what
// fileimport.ParseImportTarget parses back into the same target -- the two
// packages' independent implementations of one grammar must round-trip
// through each other, since ui/actions cannot share fileimport's type.
func TestManualAssignTargetStringMatchesFileimportGrammar(t *testing.T) {
	cases := []actions.ManualAssignTarget{
		{Kind: commonv1.MediaKindMovie, Name: "the-matrix-1999"},
		{Kind: commonv1.MediaKindAlbum, Name: "ok-computer"},
		{Kind: commonv1.MediaKindComic, Name: "saga", Key: "saga-00001.0"},
		{Kind: commonv1.MediaKindIssue, Name: "saga-00001.0"},
	}
	for _, tc := range cases {
		t.Run(tc.String(), func(t *testing.T) {
			parsed, err := fileimport.ParseImportTarget(tc.String())
			require.NoError(t, err)
			require.Equal(t, tc.Kind, parsed.Kind)
			require.Equal(t, tc.Name, parsed.Name)
			require.Equal(t, tc.Key, parsed.Key)
		})
	}
}

// TestManualAssignCreatesAnAnnotatedLibraryScan proves the exact create
// body: the annotation, spec.subpath, spec.rootFolderRef, spec.mode, the
// origin label, and generateName -- this is the "the action's exact create
// body" requirement, falsified by temporarily corrupting the annotation
// value below and confirming this test fails (see the commit message for
// how).
func TestManualAssignCreatesAnAnnotatedLibraryScan(t *testing.T) {
	w := &fakeWriter{}
	target := actions.ManualAssignTarget{Kind: commonv1.MediaKindAlbum, Name: "ok-computer"}
	scan, err := actions.ManualAssign(t.Context(), w, "media", "music", "Radiohead/OK Computer/01 Airbag.flac", target)
	require.NoError(t, err)
	require.Len(t, w.creates, 1)
	require.Empty(t, w.patches)
	require.Same(t, scan, w.creates[0].obj)

	require.Empty(t, scan.Name, "the apiserver names it")
	require.Equal(t, "assign-", scan.GenerateName)
	require.Equal(t, "media", scan.Namespace)
	require.Equal(t, map[string]string{actions.LabelOrigin: actions.OriginUI}, scan.Labels)
	require.Equal(t, map[string]string{actions.AnnotationImportTarget: "album/ok-computer"}, scan.Annotations)
	require.Equal(t, catalogv1alpha1.LibraryScanSpec{
		RootFolderRef: "music",
		Subpath:       "Radiohead/OK Computer/01 Airbag.flac",
		Mode:          catalogv1alpha1.ScanModeFull,
	}, scan.Spec)
	require.Equal(t, catalogv1alpha1.LibraryScanStatus{}, scan.Status)
	require.Equal(t, actions.FieldManager, w.creates[0].opts.FieldManager)
}

// TestManualAssignKeyedTarget proves a comic target with a key round-trips
// into the annotation's three-segment grammar.
func TestManualAssignKeyedTarget(t *testing.T) {
	w := &fakeWriter{}
	target := actions.ManualAssignTarget{Kind: commonv1.MediaKindComic, Name: "saga", Key: "saga-00001.0"}
	scan, err := actions.ManualAssign(t.Context(), w, "media", "comics", "Saga/Saga 001.cbz", target)
	require.NoError(t, err)
	require.Equal(t, "comic/saga/saga-00001.0", scan.Annotations[actions.AnnotationImportTarget])
}

// TestManualAssignRejectsInvalidInputWithoutWriting covers every shape
// [ManualAssign] must refuse before creating anything: missing
// namespace/rootFolder/subpath, an unknown target kind, an empty or
// malformed target name, and a key on a kind that has none.
func TestManualAssignRejectsInvalidInputWithoutWriting(t *testing.T) {
	valid := actions.ManualAssignTarget{Kind: commonv1.MediaKindMovie, Name: "heat"}
	cases := map[string]struct {
		namespace, rootFolder, subpath string
		target                         actions.ManualAssignTarget
	}{
		"no namespace":     {"", "movies", "Heat.mkv", valid},
		"no root folder":   {"media", "", "Heat.mkv", valid},
		"no subpath":       {"media", "movies", "", valid},
		"unknown kind":     {"media", "movies", "Heat.mkv", actions.ManualAssignTarget{Kind: "podcast", Name: "heat"}},
		"empty name":       {"media", "movies", "Heat.mkv", actions.ManualAssignTarget{Kind: commonv1.MediaKindMovie}},
		"name with spaces": {"media", "movies", "Heat.mkv", actions.ManualAssignTarget{Kind: commonv1.MediaKindMovie, Name: "not a valid name"}},
		"key on unkeyed kind": {
			"media", "movies", "Heat.mkv",
			actions.ManualAssignTarget{Kind: commonv1.MediaKindMovie, Name: "heat", Key: "x"},
		},
		"malformed key": {
			"media", "comics", "Saga/Saga 001.cbz",
			actions.ManualAssignTarget{Kind: commonv1.MediaKindComic, Name: "saga", Key: "not a valid key"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := &fakeWriter{}
			_, err := actions.ManualAssign(t.Context(), w, tc.namespace, tc.rootFolder, tc.subpath, tc.target)
			require.ErrorIs(t, err, actions.ErrInvalid)
			require.Empty(t, w.creates)
			require.Empty(t, w.patches)
		})
	}
}

// TestManualAssignWrapsTheAPIServersError mirrors
// TestActionsWrapTheAPIServersError.
func TestManualAssignWrapsTheAPIServersError(t *testing.T) {
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: "catalog.clustarr.io", Resource: "libraryscans"}, "gone")
	w := &fakeWriter{err: notFound}
	_, err := actions.ManualAssign(t.Context(), w, "media", "movies", "Heat.mkv",
		actions.ManualAssignTarget{Kind: commonv1.MediaKindMovie, Name: "heat"})
	require.ErrorIs(t, err, notFound)
}

// TestManualAssignWithNoWriterRefuses mirrors TestActionsWithNoWriterRefuse.
func TestManualAssignWithNoWriterRefuses(t *testing.T) {
	for name, a := range map[string]*actions.Actions{"nil *Actions": nil, "nil Writer": actions.New(nil)} {
		t.Run(name, func(t *testing.T) {
			_, err := a.ManualAssign(t.Context(), "media", "movies", "Heat.mkv",
				actions.ManualAssignTarget{Kind: commonv1.MediaKindMovie, Name: "heat"})
			require.ErrorIs(t, err, actions.ErrNoWriter)
		})
	}
}

// TestManualAssignGrantIsAlreadyDeclared proves the coordinator's own
// question -- "confirm whether the annotation needs anything more" -- has
// the answer the code assumes: create on libraryscans, already declared for
// Rescan, is the only grant ManualAssign needs, because annotations carry no
// RBAC meaning of their own.
func TestManualAssignGrantIsAlreadyDeclared(t *testing.T) {
	want := actions.Grant{Group: "catalog.clustarr.io", Resource: "libraryscans", Verb: "create"}
	found := false
	count := 0
	for _, g := range actions.Grants() {
		if g == want {
			found = true
		}
		if g.Resource == "libraryscans" {
			count++
		}
	}
	require.True(t, found, "actions.Grants() must already declare create on libraryscans")
	require.Equal(t, 1, count, "ManualAssign must add no new libraryscans grant beyond the one Rescan already declared")
}
