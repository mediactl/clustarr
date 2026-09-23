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
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
)

// settingsCase is one Settings-page action, exercised the same way for every
// kind: call it once against a fresh fakeWriter, and check the one merge
// patch it sent -- type, namespace (empty for a cluster-scoped kind), name,
// patch type, exact JSON body, and field manager. This is settings.go's own
// version of TestSetMonitoredSendsOneMergePatchOfOneLeaf, run over every one
// of its eight actions instead of restating the same assertions eight times.
type settingsCase struct {
	name       string
	namespaced bool
	run        func(ctx context.Context, w *fakeWriter) (client.Object, error)
	wantType   client.Object
	wantJSON   string
}

func settingsCases() []settingsCase {
	return []settingsCase{
		{
			name:       "RootFolder scanSchedule",
			namespaced: true,
			run: func(ctx context.Context, w *fakeWriter) (client.Object, error) {
				return actions.SetRootFolderScanSchedule(ctx, w, "media", "movies", "0 3 * * *")
			},
			wantType: &catalogv1alpha1.RootFolder{},
			wantJSON: `{"spec":{"scanSchedule":"0 3 * * *"}}`,
		},
		{
			name:       "QualityProfile upgradeAllowed",
			namespaced: false,
			run: func(ctx context.Context, w *fakeWriter) (client.Object, error) {
				return actions.SetQualityProfileUpgradeAllowed(ctx, w, "hd-1080p", false)
			},
			wantType: &catalogv1alpha1.QualityProfile{},
			wantJSON: `{"spec":{"upgradeAllowed":false}}`,
		},
		{
			name:       "Indexer enabled/priority",
			namespaced: true,
			run: func(ctx context.Context, w *fakeWriter) (client.Object, error) {
				return actions.SetIndexerSettings(ctx, w, "media", "1337x", false, 10)
			},
			wantType: &indexv1alpha1.Indexer{},
			wantJSON: `{"spec":{"enabled":false,"priority":10}}`,
		},
		{
			name:       "DownloadClient enabled/priority",
			namespaced: true,
			run: func(ctx context.Context, w *fakeWriter) (client.Object, error) {
				return actions.SetDownloadClientSettings(ctx, w, "media", "qbittorrent", true, 5)
			},
			wantType: &downloadv1alpha1.DownloadClient{},
			wantJSON: `{"spec":{"enabled":true,"priority":5}}`,
		},
		{
			name:       "MetadataProvider enabled/priority",
			namespaced: true,
			run: func(ctx context.Context, w *fakeWriter) (client.Object, error) {
				return actions.SetMetadataProviderSettings(ctx, w, "media", "tmdb", true, 50)
			},
			wantType: &catalogv1alpha1.MetadataProvider{},
			wantJSON: `{"spec":{"enabled":true,"priority":50}}`,
		},
		{
			name:       "SubtitleProvider enabled/priority",
			namespaced: true,
			run: func(ctx context.Context, w *fakeWriter) (client.Object, error) {
				return actions.SetSubtitleProviderSettings(ctx, w, "media", "opensubtitlescom", false, 25)
			},
			wantType: &subtitlev1alpha1.SubtitleProvider{},
			wantJSON: `{"spec":{"enabled":false,"priority":25}}`,
		},
		{
			name:       "SubtitleProfile default",
			namespaced: false,
			run: func(ctx context.Context, w *fakeWriter) (client.Object, error) {
				return actions.SetSubtitleProfileDefault(ctx, w, "english", true)
			},
			wantType: &subtitlev1alpha1.SubtitleProfile{},
			wantJSON: `{"spec":{"default":true}}`,
		},
		{
			name:       "TranscodeProfile priority",
			namespaced: false,
			run: func(ctx context.Context, w *fakeWriter) (client.Object, error) {
				return actions.SetTranscodeProfilePriority(ctx, w, "hevc-10bit", 75)
			},
			wantType: &transcodev1alpha1.TranscodeProfile{},
			wantJSON: `{"spec":{"priority":75}}`,
		},
	}
}

// TestSettingsActionsSendOneMergePatchOfTheirDeclaredFields runs every
// settingsCase and checks the shape settings.go's package doc promises: one
// merge patch, the right typed object, the exact field set, field manager
// [actions.FieldManager].
func TestSettingsActionsSendOneMergePatchOfTheirDeclaredFields(t *testing.T) {
	for _, tc := range settingsCases() {
		t.Run(tc.name, func(t *testing.T) {
			w := &fakeWriter{}
			obj, err := tc.run(t.Context(), w)
			require.NoError(t, err)
			require.Len(t, w.patches, 1)
			require.Empty(t, w.creates)

			p := w.patches[0]
			require.Same(t, obj, p.obj)
			require.IsType(t, tc.wantType, p.obj)
			require.Equal(t, types.MergePatchType, p.patchType,
				"every Settings-page action is a JSON merge patch, not an apply -- see settings.go's own doc comment")
			require.JSONEq(t, tc.wantJSON, string(p.data))
			require.Equal(t, actions.FieldManager, p.opts.FieldManager)
			require.Nil(t, p.opts.Force, "a merge patch takes no force option")
			if !tc.namespaced {
				require.Empty(t, p.obj.GetNamespace(), "%s is cluster-scoped; no namespace should be set", tc.name)
			}
		})
	}
}

// TestSettingsActionsRejectInvalidInputWithoutWriting mirrors
// TestActionsRejectInvalidInputWithoutWriting for the eight Settings-page
// actions: an empty name (and, for a namespaced kind, an empty namespace)
// must return actions.ErrInvalid and write nothing.
func TestSettingsActionsRejectInvalidInputWithoutWriting(t *testing.T) {
	cases := map[string]func(*fakeWriter) error{
		"root folder: no namespace": func(w *fakeWriter) error {
			_, err := actions.SetRootFolderScanSchedule(t.Context(), w, "", "movies", "0 3 * * *")
			return err
		},
		"root folder: no name": func(w *fakeWriter) error {
			_, err := actions.SetRootFolderScanSchedule(t.Context(), w, "media", "", "0 3 * * *")
			return err
		},
		"quality profile: no name": func(w *fakeWriter) error {
			_, err := actions.SetQualityProfileUpgradeAllowed(t.Context(), w, "", true)
			return err
		},
		"indexer: no namespace": func(w *fakeWriter) error {
			_, err := actions.SetIndexerSettings(t.Context(), w, "", "1337x", true, 25)
			return err
		},
		"indexer: no name": func(w *fakeWriter) error {
			_, err := actions.SetIndexerSettings(t.Context(), w, "media", "", true, 25)
			return err
		},
		"download client: no namespace": func(w *fakeWriter) error {
			_, err := actions.SetDownloadClientSettings(t.Context(), w, "", "qbittorrent", true, 1)
			return err
		},
		"metadata provider: no name": func(w *fakeWriter) error {
			_, err := actions.SetMetadataProviderSettings(t.Context(), w, "media", "", true, 50)
			return err
		},
		"subtitle provider: no namespace": func(w *fakeWriter) error {
			_, err := actions.SetSubtitleProviderSettings(t.Context(), w, "", "opensubtitlescom", true, 50)
			return err
		},
		"subtitle profile: no name": func(w *fakeWriter) error {
			_, err := actions.SetSubtitleProfileDefault(t.Context(), w, "", true)
			return err
		},
		"transcode profile: no name": func(w *fakeWriter) error {
			_, err := actions.SetTranscodeProfilePriority(t.Context(), w, "", 50)
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

// TestSettingsActionsWrapTheAPIServersError proves a settings action's error
// still satisfies apierrors.IsNotFound (and errors.Is against the original),
// the same contract TestActionsWrapTheAPIServersError holds the §A3.2
// actions to -- CEL-rejected patches (an immutable field, a built-in
// QualityProfile) surface the same way, since both are just "the apiserver
// returned an error to Patch".
func TestSettingsActionsWrapTheAPIServersError(t *testing.T) {
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: "index.clustarr.io", Resource: "indexers"}, "gone")
	w := &fakeWriter{err: notFound}

	_, err := actions.SetIndexerSettings(t.Context(), w, "media", "gone", true, 25)
	require.True(t, apierrors.IsNotFound(err), "want NotFound, got %v", err)
	require.ErrorIs(t, err, notFound)
}

// TestSettingsActionsWithNoWriterRefuse mirrors TestActionsWithNoWriterRefuse
// for every Settings-page method on *actions.Actions.
func TestSettingsActionsWithNoWriterRefuse(t *testing.T) {
	for name, a := range map[string]*actions.Actions{"nil *Actions": nil, "nil Writer": actions.New(nil)} {
		t.Run(name, func(t *testing.T) {
			_, err := a.SetRootFolderScanSchedule(t.Context(), "media", "movies", "0 3 * * *")
			require.ErrorIs(t, err, actions.ErrNoWriter)
			_, err = a.SetQualityProfileUpgradeAllowed(t.Context(), "hd-1080p", true)
			require.ErrorIs(t, err, actions.ErrNoWriter)
			_, err = a.SetIndexerSettings(t.Context(), "media", "1337x", true, 25)
			require.ErrorIs(t, err, actions.ErrNoWriter)
			_, err = a.SetDownloadClientSettings(t.Context(), "media", "qbittorrent", true, 1)
			require.ErrorIs(t, err, actions.ErrNoWriter)
			_, err = a.SetMetadataProviderSettings(t.Context(), "media", "tmdb", true, 50)
			require.ErrorIs(t, err, actions.ErrNoWriter)
			_, err = a.SetSubtitleProviderSettings(t.Context(), "media", "opensubtitlescom", true, 50)
			require.ErrorIs(t, err, actions.ErrNoWriter)
			_, err = a.SetSubtitleProfileDefault(t.Context(), "english", true)
			require.ErrorIs(t, err, actions.ErrNoWriter)
			_, err = a.SetTranscodeProfilePriority(t.Context(), "hevc-10bit", 50)
			require.ErrorIs(t, err, actions.ErrNoWriter)
		})
	}
}

// TestSettingsGrantsAreASubsetOfActionsGrants proves settings.go's grants
// were actually wired into actions.Grants() (ui_rbac_test.go then holds the
// role file to the whole of Grants(), settings grants included).
func TestSettingsGrantsAreASubsetOfActionsGrants(t *testing.T) {
	want := []actions.Grant{
		{Group: "catalog.clustarr.io", Resource: "rootfolders", Verb: "patch"},
		{Group: "catalog.clustarr.io", Resource: "qualityprofiles", Verb: "patch"},
		{Group: "catalog.clustarr.io", Resource: "metadataproviders", Verb: "patch"},
		{Group: "index.clustarr.io", Resource: "indexers", Verb: "patch"},
		{Group: "download.clustarr.io", Resource: "downloadclients", Verb: "patch"},
		{Group: "subtitle.clustarr.io", Resource: "subtitleproviders", Verb: "patch"},
		{Group: "subtitle.clustarr.io", Resource: "subtitleprofiles", Verb: "patch"},
		{Group: "transcode.clustarr.io", Resource: "transcodeprofiles", Verb: "patch"},
	}
	got := map[actions.Grant]bool{}
	for _, g := range actions.Grants() {
		got[g] = true
	}
	for _, g := range want {
		require.True(t, got[g], "actions.Grants() is missing %+v", g)
	}
}
