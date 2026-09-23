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

package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// builderIndexer lets RegisterDownloadIndexes register onto a fake client.
type builderIndexer struct{ b *fake.ClientBuilder }

func (i builderIndexer) IndexField(_ context.Context, obj client.Object, field string, fn client.IndexerFunc) error {
	i.b.WithIndex(obj, field, fn)
	return nil
}

var blNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func blocklistedDownload(name, hash, title string, until *time.Time) *downloadv1alpha1.Download {
	d := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "ns",
			Labels: map[string]string{downloadv1alpha1.LabelBlocklisted: downloadv1alpha1.LabelBlocklistedValue},
		},
		Spec: downloadv1alpha1.DownloadSpec{Release: commonv1.ReleaseInfo{InfoHash: hash, Title: title}},
	}
	if until != nil {
		t := metav1.NewTime(*until)
		d.Status.BlocklistedUntil = &t
	}
	return d
}

// blocklistClient is a fake client holding a small blocklist, counting every
// List that selects on the blocklisted label.
func blocklistClient(t *testing.T, failBlocklist bool) (client.Client, *atomic.Int32) {
	t.Helper()
	future, past := blNow.Add(time.Hour), blNow.Add(-time.Hour)
	unlabelled := blocklistedDownload("unlabelled", "cccccccccccccccccccccccccccccccccccccccc", "Other.Movie.2001.1080p-GRP", nil)
	unlabelled.Labels = nil

	b := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
		&catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "the-matrix", Namespace: "ns"},
			Spec:       catalogv1alpha1.MovieSpec{TmdbID: 603, QualityProfileRef: "hd", RootFolderRef: "movies"},
		},
		blocklistedDownload("by-hash", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "", &future),
		blocklistedDownload("by-title", "", "The.Matrix.1999.720p.BluRay.x264-BAD", nil),
		blocklistedDownload("non-latin", "", "マトリックス.1999.1080p-GRP", &future),
		blocklistedDownload("expired", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "Expired.Title.2020-GRP", &past),
		unlabelled,
	)
	require.NoError(t, RegisterDownloadIndexes(context.Background(), builderIndexer{b}))

	var blocklistLists atomic.Int32
	c := interceptor.NewClient(b.Build(), interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			lo := (&client.ListOptions{}).ApplyOptions(opts)
			if lo.LabelSelector != nil && strings.Contains(lo.LabelSelector.String(), downloadv1alpha1.LabelBlocklisted) {
				blocklistLists.Add(1)
				if failBlocklist {
					return errors.New("cache unavailable")
				}
			}
			return c.List(ctx, list, opts...)
		},
	})
	return c, &blocklistLists
}

// TestSnapshotLoadsTheBlocklistOncePerSearch pins the carried C-phase N+1:
// the predicate ran two cache Lists per candidate release, so a 500-release
// reply cost up to a thousand Lists. It is now one List per search, however
// many releases are checked.
func TestSnapshotLoadsTheBlocklistOncePerSearch(t *testing.T) {
	c, lists := blocklistClient(t, false)
	w := &Worker{Client: c, Clock: clockwork.NewFakeClockAt(blNow)}

	snap, err := w.snapshot(context.Background(), "ns", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"})
	require.NoError(t, err)
	require.NotNil(t, snap.Target.Blocklist)

	for i := range 400 {
		snap.Target.Blocklist(fmt.Sprintf("%040x", i), fmt.Sprintf("Some.Release.%d.1080p-GRP", i))
	}
	require.Equal(t, int32(1), lists.Load(), "one List of the blocklist per search, not one per release")

	bl := snap.Target.Blocklist
	require.True(t, bl("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ""), "info hashes compare case-insensitively")
	require.True(t, bl("", "the matrix 1999 720p bluray x264-bad"), "titles compare normalized")
	require.True(t, bl("", "マトリックス.1999.1080p-grp"), "a non-Latin title is blocklistable by title")
	require.False(t, bl("", "ダークナイト.1999.1080p-GRP"),
		"a different non-Latin title from the same year and group is not: CleanTitle keyed both as \"1999 1080pgrp\"")
	require.False(t, bl("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "Expired.Title.2020-GRP"), "an expired entry is not blocklisted")
	require.False(t, bl("cccccccccccccccccccccccccccccccccccccccc", "Other.Movie.2001.1080p-GRP"), "an unlabelled Download is not on the blocklist")
	require.False(t, bl("", ""), "nothing matches nothing")
}

// TestSnapshotFailsRatherThanIgnoringTheBlocklist: the per-release form
// read a failed lookup as "not blocklisted", so a cache blip could approve a
// release an operator had blocklisted. A failed load now fails the search,
// which redelivers.
func TestSnapshotFailsRatherThanIgnoringTheBlocklist(t *testing.T) {
	c, _ := blocklistClient(t, true)
	w := &Worker{Client: c, Clock: clockwork.NewFakeClockAt(blNow)}
	_, err := w.snapshot(context.Background(), "ns", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"})
	require.ErrorContains(t, err, "cache unavailable")
}
