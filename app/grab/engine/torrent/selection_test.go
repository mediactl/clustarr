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

package torrent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func episode(ns, name string, season, number int32, mutate func(*catalogv1alpha1.Episode)) *catalogv1alpha1.Episode {
	ep := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "show", SeasonNumber: season, EpisodeNumber: number},
	}
	if mutate != nil {
		mutate(ep)
	}
	return ep
}

func episodeReader(objs ...client.Object) client.Reader {
	return fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).Build()
}

// TestSelectionWantsOnlyTheTargetedEpisodes is the carried item's case end
// to end through the pure half: a season pack grabbed for S01E03 alone
// wants S01E03's video and its subtitle, skips the other episodes, and keeps
// what names no episode at all.
func TestSelectionWantsOnlyTheTargetedEpisodes(t *testing.T) {
	r := episodeReader(episode("tv", "show-s01e03", 1, 3, nil))
	sel, err := resolveSelection(context.Background(), r, downloadTarget{
		namespace: "tv",
		target:    commonv1alpha1.MediaRef{Kind: commonv1alpha1.MediaKindEpisode, Name: "show-s01e03"},
	})
	require.NoError(t, err)
	want := sel.selector()
	require.NotNil(t, want)

	cases := map[string]bool{
		"Show.S01.1080p.WEB-DL/Show.S01E03.1080p.WEB-DL.mkv":         true,
		"Show.S01.1080p.WEB-DL/Subs/Show.S01E03.1080p.WEB-DL.en.srt": true,
		"Show.S01.1080p.WEB-DL/Show.S01E01.1080p.WEB-DL.mkv":         false,
		"Show.S01.1080p.WEB-DL/Show.S01E04.1080p.WEB-DL.mkv":         false,
		"Show.S01.1080p.WEB-DL/Show.S02E03.1080p.WEB-DL.mkv":         false,
		"Show.S01.1080p.WEB-DL/Show.S01E02E03.1080p.WEB-DL.mkv":      true, // a multi-episode file holding the wanted one
		"Show.S01.1080p.WEB-DL/Show.S01.1080p.WEB-DL.nfo":            true, // names no episode: kept
		"Show.S01.1080p.WEB-DL/featurette.mkv":                       true,
	}
	for path, wanted := range cases {
		assert.Equal(t, wanted, want(path, 1), path)
	}
}

// TestSelectionAcceptsSceneAndAbsoluteNumbering proves the selection is the
// union of every numbering the catalog knows, because a pack's release group
// chose one of them and nothing here can tell which.
func TestSelectionAcceptsSceneAndAbsoluteNumbering(t *testing.T) {
	r := episodeReader(
		episode("tv", "show-s01e04", 1, 4, func(ep *catalogv1alpha1.Episode) {
			ep.Status.AbsoluteNumber = ptr.To[int32](16)
			ep.Status.SceneNumbering = &catalogv1alpha1.SceneNumbering{Episode: ptr.To[int32](3), Absolute: ptr.To[int32](15)}
		}),
		episode("tv", "show-s01e05", 1, 5, nil),
	)
	sel, err := resolveSelection(context.Background(), r, downloadTarget{
		namespace: "tv",
		target: commonv1alpha1.MediaRef{
			Kind: commonv1alpha1.MediaKindSeries, Name: "show", Keys: []string{"show-s01e04", "show-s01e05"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []EpisodeNumber{{1, 3}, {1, 4}, {1, 5}}, sel.Episodes)
	assert.Equal(t, []int{15, 16}, sel.Absolutes)

	want := sel.selector()
	assert.True(t, want("Pack/Show.S01E03.mkv", 1), "scene number of a wanted episode")
	assert.True(t, want("Pack/Show.S01E05.mkv", 1), "official number of a wanted episode")
	assert.False(t, want("Pack/Show.S01E06.mkv", 1))
	assert.True(t, want("[Group] Show - 16 [1080p].mkv", 1), "official absolute")
	assert.True(t, want("[Group] Show - 15 [1080p].mkv", 1), "scene absolute")
	assert.False(t, want("[Group] Show - 17 [1080p].mkv", 1))
}

// TestSelectionMatchesDailyEpisodesByAirDate uses the UTC calendar day of
// status.airDate, with a day either side, whatever zone it decoded into.
func TestSelectionMatchesDailyEpisodesByAirDate(t *testing.T) {
	west := time.FixedZone("UTC-8", -8*3600)
	// 2026-01-01 00:30 UTC reads as 2025-12-31 in UTC-8.
	aired := metav1.NewTime(time.Date(2026, 1, 1, 0, 30, 0, 0, time.UTC).In(west))
	r := episodeReader(episode("tv", "show-2026-01-01", 2026, 1, func(ep *catalogv1alpha1.Episode) {
		ep.Status.AirDate = &aired
	}))
	sel, err := resolveSelection(context.Background(), r, downloadTarget{
		namespace: "tv",
		target:    commonv1alpha1.MediaRef{Kind: commonv1alpha1.MediaKindEpisode, Name: "show-2026-01-01"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"2025-12-31", "2026-01-01", "2026-01-02"}, sel.AirDates)

	want := sel.selector()
	assert.True(t, want("Pack/Show.2026.01.01.720p.HDTV.mkv", 1))
	assert.False(t, want("Pack/Show.2026.01.05.720p.HDTV.mkv", 1))
}

// TestResolveSelectionLeavesNonEpisodeTargetsWhole covers every "no
// selection" answer: a movie, a pack with no keys, no reader configured.
func TestResolveSelectionLeavesNonEpisodeTargetsWhole(t *testing.T) {
	r := episodeReader()
	for name, tc := range map[string]struct {
		reader client.Reader
		target commonv1alpha1.MediaRef
	}{
		"movie":            {r, commonv1alpha1.MediaRef{Kind: commonv1alpha1.MediaKindMovie, Name: "arrival"}},
		"series, no keys":  {r, commonv1alpha1.MediaRef{Kind: commonv1alpha1.MediaKindSeries, Name: "show"}},
		"album":            {r, commonv1alpha1.MediaRef{Kind: commonv1alpha1.MediaKindAlbum, Name: "album"}},
		"no reader at all": {nil, commonv1alpha1.MediaRef{Kind: commonv1alpha1.MediaKindEpisode, Name: "show-s01e01"}},
	} {
		sel, err := resolveSelection(context.Background(), tc.reader, downloadTarget{namespace: "tv", target: tc.target})
		require.NoError(t, err, name)
		assert.Nil(t, sel.selector(), name)
	}
}

// TestResolveSelectionFailsWholeOnAMissingEpisode: a selection built from
// part of a pack would skip the files of the episodes it could not read, so
// one unreadable key is an error the caller degrades to "every file".
func TestResolveSelectionFailsWholeOnAMissingEpisode(t *testing.T) {
	r := episodeReader(episode("tv", "show-s01e01", 1, 1, nil))
	sel, err := resolveSelection(context.Background(), r, downloadTarget{
		namespace: "tv",
		target: commonv1alpha1.MediaRef{
			Kind: commonv1alpha1.MediaKindSeries, Name: "show", Keys: []string{"show-s01e01", "show-s01e02"},
		},
	})
	require.Error(t, err)
	assert.Nil(t, sel)
}
