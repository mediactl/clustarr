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
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/ui/actions"
)

// TestDownloadActionsPatchOnlyTheOwnersAnnotations: the download intents
// (ADR-0019 §6.10, A3.9) are each one merge patch, under the ui's field
// manager, of one annotation on the grab's owner -- no spec, no status, no
// other annotation -- in the grammar the downloads stage parses
// (app/grab/lifecycle.ParseIntents, read back here as the real consumer).
// A standing intent carries the resourceVersion it was read at and keeps
// every other entry it names; a kind that holds no grabs, and an id, a
// priority, a target or a release the grammar cannot carry, are refused
// without a write.
func TestDownloadActionsPatchOnlyTheOwnersAnnotations(t *testing.T) {
	ctx := context.Background()
	now := time.UnixMilli(1_760_000_000_123)
	nonce := strconv.FormatInt(now.UnixMilli(), 10)
	cur := actions.Standing{Value: "e0", ResourceVersion: "7"}

	for _, tc := range []struct {
		name      string
		do        func(p actions.Patcher) error
		key       string
		value     string
		rv        string
		parsedAre func(t *testing.T, in lifecycle.Intents)
	}{
		{
			name: "pause keeps the other paused entries",
			do: func(p actions.Patcher) error {
				return actions.PauseDownload(ctx, p, "default", commonv1.MediaKindMovie, "heat", "e1", true, cur)
			},
			key: catalogv1alpha1.AnnotationDownloadPaused, value: "e0,e1", rv: "7",
			parsedAre: func(t *testing.T, in lifecycle.Intents) {
				require.Equal(t, map[string]bool{"e0": true, "e1": true}, in.Paused)
			},
		},
		{
			name: "priority replaces only its own entry's",
			do: func(p actions.Patcher) error {
				return actions.SetDownloadPriority(ctx, p, "default", commonv1.MediaKindMovie, "heat", "e1", "high",
					actions.Standing{Value: "e0=low,e1=normal", ResourceVersion: "7"})
			},
			key: catalogv1alpha1.AnnotationDownloadPriority, value: "e0=low,e1=high", rv: "7",
			parsedAre: func(t *testing.T, in lifecycle.Intents) {
				require.Equal(t, map[string]string{"e0": "low", "e1": "high"}, in.Priority)
			},
		},
		{
			name: "remove with its data, blocklisted",
			do: func(p actions.Patcher) error {
				return actions.RemoveDownload(ctx, p, "default", commonv1.MediaKindMovie, "heat", "e1", true, true, now)
			},
			key: catalogv1alpha1.AnnotationDownloadRemove, value: nonce + " e1 data blocklist",
			parsedAre: func(t *testing.T, in lifecycle.Intents) {
				require.Equal(t, &lifecycle.RemoveIntent{Nonce: nonce, ID: "e1", Data: true, Blocklist: true}, in.Remove)
			},
		},
		{
			name: "resume",
			do: func(p actions.Patcher) error {
				return actions.ResumeDownload(ctx, p, "default", commonv1.MediaKindMovie, "heat", "e1", now)
			},
			key: catalogv1alpha1.AnnotationDownloadResume, value: nonce + " e1",
			parsedAre: func(t *testing.T, in lifecycle.Intents) {
				require.Equal(t, &lifecycle.ResumeIntent{Nonce: nonce, ID: "e1"}, in.Resume)
			},
		},
		{
			name: "retry an import into one episode of a pack, as a person",
			do: func(p actions.Patcher) error {
				return actions.RetryImport(ctx, p, "default", commonv1.MediaKindSeries, "andor", "e1",
					&actions.ImportTarget{Kind: commonv1.MediaKindEpisode, Name: "andor-s01e02"}, true, now)
			},
			key: catalogv1alpha1.AnnotationDownloadImport, value: nonce + " e1 target=episode/andor-s01e02 override",
			parsedAre: func(t *testing.T, in lifecycle.Intents) {
				require.NotNil(t, in.Import)
				require.Equal(t, "e1", in.Import.ID)
				require.True(t, in.Import.Override)
				require.NotNil(t, in.Import.Target)
				require.Equal(t, "Episode", in.Import.Target.Kind)
				require.Equal(t, "andor-s01e02", in.Import.Target.Name)
			},
		},
		{
			name: "unblock a usenet release for every item",
			do: func(p actions.Patcher) error {
				return actions.UnblockRelease(ctx, p, "default", commonv1.MediaKindMovie, "heat", "nzbgeek/guid-1", true, now)
			},
			key: catalogv1alpha1.AnnotationDownloadUnblock, value: nonce + " nzbgeek/guid-1 global",
			parsedAre: func(t *testing.T, in lifecycle.Intents) {
				require.Equal(t, &lifecycle.UnblockIntent{Nonce: nonce, Indexer: "nzbgeek", GUID: "guid-1", Global: true}, in.Unblock)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &fakeWriter{}
			require.NoError(t, tc.do(w))
			require.Empty(t, w.creates)
			require.Empty(t, w.deletes)
			require.Len(t, w.patches, 1, "one intent, one patch")
			p := w.patches[0]
			require.Equal(t, types.MergePatchType, p.patchType)
			require.Equal(t, actions.FieldManager, p.opts.FieldManager)

			var body map[string]any
			require.NoError(t, json.Unmarshal(p.data, &body))
			require.Equal(t, []string{"metadata"}, keysOf(body), "the patch touches metadata alone: no spec, no status")
			md := body["metadata"].(map[string]any)
			wantMD := map[string]any{"annotations": map[string]any{tc.key: tc.value}}
			if tc.rv != "" {
				wantMD["resourceVersion"] = tc.rv
			}
			require.Equal(t, wantMD, md, "exactly one annotation, and the read resourceVersion for a standing intent")

			require.Empty(t, lifecycle.ParseIntents(map[string]string{tc.key: tc.value}).Invalid,
				"the downloads stage must parse what the ui writes")
			tc.parsedAre(t, lifecycle.ParseIntents(map[string]string{tc.key: tc.value}))
		})
	}

	t.Run("on a real object: other annotations, spec and the last entry", func(t *testing.T) {
		scheme := runtime.NewScheme()
		require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
		movie := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{
				Name: "heat", Namespace: "default",
				Annotations: map[string]string{catalogv1alpha1.AnnotationDownloadPaused: "e0", "keep": "me"},
			},
			Spec: catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd", RootFolderRef: "movies", Monitored: new(true)},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(movie).Build()
		read := func() *catalogv1alpha1.Movie {
			var m catalogv1alpha1.Movie
			require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "heat"}, &m))
			return &m
		}
		standing := func(m *catalogv1alpha1.Movie) actions.Standing {
			return actions.Standing{Value: m.Annotations[catalogv1alpha1.AnnotationDownloadPaused], ResourceVersion: m.ResourceVersion}
		}

		m := read()
		require.NoError(t, actions.PauseDownload(ctx, c, "default", commonv1.MediaKindMovie, "heat", "e1", true, standing(m)))
		got := read()
		require.Equal(t, "e0,e1", got.Annotations[catalogv1alpha1.AnnotationDownloadPaused])
		require.Equal(t, "me", got.Annotations["keep"])
		require.Equal(t, movie.Spec, got.Spec, "an intent never touches spec")

		err := actions.PauseDownload(ctx, c, "default", commonv1.MediaKindMovie, "heat", "e2", true, standing(m))
		require.True(t, apierrors.IsConflict(err), "a toggle read before another's write conflicts rather than overwrite it: %v", err)
		require.Equal(t, "e0,e1", read().Annotations[catalogv1alpha1.AnnotationDownloadPaused])

		for _, id := range []string{"e0", "e1"} {
			require.NoError(t, actions.PauseDownload(ctx, c, "default", commonv1.MediaKindMovie, "heat", id, false, standing(read())))
		}
		got = read()
		require.NotContains(t, got.Annotations, catalogv1alpha1.AnnotationDownloadPaused, "the last unpause removes the annotation")
		require.Equal(t, "me", got.Annotations["keep"])
	})

	t.Run("refused without a write", func(t *testing.T) {
		for name, do := range map[string]func(p actions.Patcher) error{
			"an episode holds no grabs": func(p actions.Patcher) error {
				return actions.ResumeDownload(ctx, p, "default", commonv1.MediaKindEpisode, "andor-s01e02", "e1", now)
			},
			"an issue holds no grabs": func(p actions.Patcher) error {
				return actions.ResumeDownload(ctx, p, "default", commonv1.MediaKindIssue, "saga-1", "e1", now)
			},
			"no namespace": func(p actions.Patcher) error {
				return actions.ResumeDownload(ctx, p, "", commonv1.MediaKindMovie, "heat", "e1", now)
			},
			"an empty entry id": func(p actions.Patcher) error {
				return actions.RemoveDownload(ctx, p, "default", commonv1.MediaKindMovie, "heat", "", false, false, now)
			},
			"an entry id with a comma": func(p actions.Patcher) error {
				return actions.PauseDownload(ctx, p, "default", commonv1.MediaKindMovie, "heat", "e1,e2", true, actions.Standing{})
			},
			"an entry id with a space": func(p actions.Patcher) error {
				return actions.ResumeDownload(ctx, p, "default", commonv1.MediaKindMovie, "heat", "e1 data", now)
			},
			"an entry id with an equals sign": func(p actions.Patcher) error {
				return actions.SetDownloadPriority(ctx, p, "default", commonv1.MediaKindMovie, "heat", "e1=low", "high", actions.Standing{})
			},
			"a priority the stage does not know": func(p actions.Patcher) error {
				return actions.SetDownloadPriority(ctx, p, "default", commonv1.MediaKindMovie, "heat", "e1", "urgent", actions.Standing{})
			},
			"a target of no media kind": func(p actions.Patcher) error {
				return actions.RetryImport(ctx, p, "default", commonv1.MediaKindSeries, "andor", "e1",
					&actions.ImportTarget{Kind: "film", Name: "x"}, false, now)
			},
			"a target name with a slash": func(p actions.Patcher) error {
				return actions.RetryImport(ctx, p, "default", commonv1.MediaKindSeries, "andor", "e1",
					&actions.ImportTarget{Kind: commonv1.MediaKindEpisode, Name: "a/b"}, false, now)
			},
			"a target with no name": func(p actions.Patcher) error {
				return actions.RetryImport(ctx, p, "default", commonv1.MediaKindSeries, "andor", "e1",
					&actions.ImportTarget{Kind: commonv1.MediaKindEpisode}, false, now)
			},
			"an unblock with a space": func(p actions.Patcher) error {
				return actions.UnblockRelease(ctx, p, "default", commonv1.MediaKindMovie, "heat", "abc def", false, now)
			},
			"an empty unblock": func(p actions.Patcher) error {
				return actions.UnblockRelease(ctx, p, "default", commonv1.MediaKindMovie, "heat", "", false, now)
			},
		} {
			w := &fakeWriter{}
			require.ErrorIs(t, do(w), actions.ErrInvalid, name)
			require.Empty(t, w.patches, "%s: refused, so nothing is written", name)
		}
	})
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
