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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

func release(guid string, approved bool, rejections ...commonv1.Rejection) commonv1.ReleaseDecision {
	return commonv1.ReleaseDecision{
		ReleaseInfo: commonv1.ReleaseInfo{
			Protocol: commonv1.ProtocolTorrent, GUID: guid, Title: guid,
			DownloadURL: "https://tracker.example/" + guid + ".torrent",
		},
		Approved:   approved,
		Rejections: rejections,
	}
}

func fakeReconciler(t *testing.T, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).
		WithStatusSubresource(&catalogv1alpha1.Search{}).Build()
	return &Reconciler{Client: c, Scheme: c.Scheme()}, c
}

func completedBookSearch(results ...commonv1.ReleaseDecision) *catalogv1alpha1.Search {
	finished := metav1.NewTime(time.Now())
	return &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "the-idiot-x", Namespace: "media"},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "the-idiot"},
			GrabBest: true,
		},
		Status: catalogv1alpha1.SearchStatus{
			Phase: catalogv1alpha1.SearchPhaseCompleted, StartedAt: &finished, FinishedAt: &finished, Results: results,
		},
	}
}

func reconcileSearch(t *testing.T, r *Reconciler, name string) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "media", Name: name}})
	require.NoError(t, err)
}

// TestGrabBestPicksTheTopApprovedRelease: with grabBest a completed Search
// picks its best approved release, skipping a rejected one ranked above it.
// Since ADR-0019 (A4.3) the pick is sent to the owner as an automatic
// search's Candidate (grabbedBy search, not manual) and the owner's grab
// planner makes the grab, so what this package decides is the pick.
func TestGrabBestPicksTheTopApprovedRelease(t *testing.T) {
	s := completedBookSearch(
		release("rejected-first", false, commonv1.Rejection{Reason: "WrongItem", Type: commonv1.RejectionPermanent}),
		release("best", true), release("second", true))
	require.Equal(t, []string{"best"}, grabGUIDs(s))
}

// TestGrabBestNeverPicksARejectedRelease: a temporary rejection ("already
// in the queue") is a human's call on an interactive Search, never an
// automatic grab's.
func TestGrabBestNeverPicksARejectedRelease(t *testing.T) {
	s := completedBookSearch(
		release("queued", false, commonv1.Rejection{Reason: "AlreadyQueued", Type: commonv1.RejectionTemporary}))
	require.Empty(t, grabGUIDs(s))
}

// TestSearchNowNeverGrabsOverATranscodedFileButAPersonsPickMay builds its
// results the way the worker does -- decision.Evaluate on a user-invoked
// search, which "Search now" is, mapped field for field as RankAndCap maps
// them -- against a movie whose file is transcoded. Before 2026-10-07 the
// decision approved such a release for a user-invoked search, spec.grabBest
// grabbed it as grabbedBy search, and the importer, holding automatic grabs
// to the transcoded rule, refused the 20 GB file and blocklisted the release.
// Now Search now grabs nothing, and a person who picks the release by hand
// (spec.grab, the interactive grab the importer accepts) still gets it
// without spec.override.
func TestSearchNowNeverGrabsOverATranscodedFileButAPersonsPickMay(t *testing.T) {
	bluray2160, ok := quality.Lookup("video", "Bluray-2160p")
	require.True(t, ok)
	bluray1080, ok := quality.Lookup("video", "Bluray-1080p")
	require.True(t, ok)
	profile := quality.Profile{
		Tiers:                 [][]quality.Definition{{bluray2160}, {bluray1080}},
		CutoffIndex:           0,
		UpgradeAllowed:        true,
		CutoffFormatScore:     10000,
		MinUpgradeFormatScore: 1,
		ProperPolicy:          "preferAndUpgrade",
		LanguageName:          "any",
	}
	rel := commonv1.ReleaseInfo{
		GUID: "idx:heat-uhd", IndexerRef: "idx", Protocol: commonv1.ProtocolTorrent,
		Title: "Heat.1995.2160p.UHD.BluRay.x265-GROUP",
	}
	searchNow := decision.Options{UserInvoked: true, ProtocolsEnabled: map[string]bool{"torrent": true}}
	results := func(transcoded bool) []commonv1.ReleaseDecision {
		ds := decision.Evaluate(context.Background(), decision.Target{
			Kind: commonv1.MediaKindMovie, Available: true, OriginalLanguageTag: "en",
			Identity: decision.Identity{Titles: []string{"Heat"}, Year: 1995},
			Current:  &decision.Current{Quality: bluray1080.Quality, Transcoded: transcoded},
		}, profile, &catalogue.Catalogue{}, []commonv1.ReleaseInfo{rel}, searchNow)
		require.Len(t, ds, 1)
		return []commonv1.ReleaseDecision{{
			ReleaseInfo:         ds[0].Release,
			Approved:            ds[0].Approved,
			TemporarilyRejected: ds[0].TemporarilyRejected,
			Rejections:          ds[0].Rejections,
			Rank:                1,
		}}
	}
	search := func(rs []commonv1.ReleaseDecision) *catalogv1alpha1.Search {
		return &catalogv1alpha1.Search{
			Spec: catalogv1alpha1.SearchSpec{
				MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"},
				GrabBest: true,
			},
			Status: catalogv1alpha1.SearchStatus{Results: rs},
		}
	}

	// The control: over the same file untranscoded, Search now takes it.
	require.Equal(t, []string{rel.GUID}, grabGUIDs(search(results(false))))

	transcoded := results(true)
	require.Empty(t, grabGUIDs(search(transcoded)),
		"Search now must not grab over a transcoded file: the importer refuses an automatic grab there and blocklists the release")

	got := resolveGrab(rel.GUID, transcoded, false)
	require.True(t, got.Allowed, "a person's own pick over a transcoded file needs no override: %s", got.Error)
	require.Empty(t, got.Error)
}
