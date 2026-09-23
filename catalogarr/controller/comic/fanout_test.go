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

package comic_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/comic"
	"github.com/mediactl/clustarr/pkg/metadata"
)

func newComic(name string, monitorNewIssues *bool, unmonitored ...string) *catalogv1alpha1.Comic {
	return &catalogv1alpha1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "comics"},
		Spec: catalogv1alpha1.ComicSpec{
			Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-1",
			MonitorNewIssues: monitorNewIssues, UnmonitoredIssues: unmonitored,
		},
	}
}

func boolPtr(b bool) *bool { return &b }

// TestDesiredIssuesInitialFanOut: on a Comic that has never completed an
// issue sync (issuesSynced=false), every discovered issue is monitored
// unless explicitly listed in spec.unmonitoredIssues -- spec.monitorNewIssues
// plays no part yet, matching DesiredIssues' documented precedence.
func TestDesiredIssuesInitialFanOut(t *testing.T) {
	c := newComic("batman", boolPtr(false), "2")
	issues := []metadata.ComicIssue{
		{IDs: metadata.ExternalIDs{metadata.KeyComicVine: "1"}, Number: "1", Title: "Issue One"},
		{IDs: metadata.ExternalIDs{metadata.KeyComicVine: "2"}, Number: "2", Title: "Issue Two"},
	}

	got := comic.DesiredIssues(c, false, map[string]bool{}, issues)
	require.Len(t, got, 2)

	byNumber := map[string]comic.DesiredIssue{}
	for _, d := range got {
		byNumber[d.Number] = d
	}
	require.NotNil(t, byNumber["1"].Monitored)
	assert.True(t, *byNumber["1"].Monitored, "issue 1 is not in unmonitoredIssues, so the initial fan-out monitors it despite monitorNewIssues=false")
	require.NotNil(t, byNumber["2"].Monitored)
	assert.False(t, *byNumber["2"].Monitored, "issue 2 is explicitly excluded via spec.unmonitoredIssues")

	assert.Equal(t, "batman-001.0", byNumber["1"].Name)
	assert.Equal(t, "1", byNumber["1"].SourceID)
	assert.Equal(t, "Issue One", byNumber["1"].Title)
}

// TestDesiredIssuesLaterRefreshFollowsMonitorNewIssues: once issuesSynced is
// true, a genuinely new issue (not in existingNames) follows
// spec.monitorNewIssues rather than the initial "monitor everything" policy.
func TestDesiredIssuesLaterRefreshFollowsMonitorNewIssues(t *testing.T) {
	c := newComic("batman", boolPtr(false))
	issues := []metadata.ComicIssue{
		{IDs: metadata.ExternalIDs{metadata.KeyComicVine: "3"}, Number: "3", Title: "Issue Three"},
	}

	got := comic.DesiredIssues(c, true, map[string]bool{}, issues)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].Monitored)
	assert.False(t, *got[0].Monitored, "a new issue after the first sync must follow monitorNewIssues=false")
}

// TestDesiredIssuesDefaultMonitorNewIssuesIsTrue: a nil spec.monitorNewIssues
// defaults to true, matching IssueSpec.Monitored's own
// +kubebuilder:default=true and ComicSpec.MonitorNewIssues' doc comment.
func TestDesiredIssuesDefaultMonitorNewIssuesIsTrue(t *testing.T) {
	c := newComic("batman", nil)
	issues := []metadata.ComicIssue{{IDs: metadata.ExternalIDs{metadata.KeyComicVine: "4"}, Number: "4"}}

	got := comic.DesiredIssues(c, true, map[string]bool{}, issues)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].Monitored)
	assert.True(t, *got[0].Monitored)
}

// TestDesiredIssuesExistingIssueMonitoredIsNil: an issue whose object already
// exists must never get a non-nil Monitored -- the user owns spec.monitored
// once created.
func TestDesiredIssuesExistingIssueMonitoredIsNil(t *testing.T) {
	c := newComic("batman", boolPtr(true))
	issues := []metadata.ComicIssue{{IDs: metadata.ExternalIDs{metadata.KeyComicVine: "1"}, Number: "1"}}

	got := comic.DesiredIssues(c, true, map[string]bool{"batman-001.0": true}, issues)
	require.Len(t, got, 1)
	assert.Nil(t, got[0].Monitored)
}

// TestDesiredIssuesDedupeByNumber: a duplicate Number is deduplicated,
// first occurrence wins, the same defensive posture as
// series.DesiredEpisodes.
func TestDesiredIssuesDedupeByNumber(t *testing.T) {
	c := newComic("batman", boolPtr(true))
	issues := []metadata.ComicIssue{
		{IDs: metadata.ExternalIDs{metadata.KeyComicVine: "1"}, Number: "1", Title: "First"},
		{IDs: metadata.ExternalIDs{metadata.KeyComicVine: "1dup"}, Number: "1", Title: "Duplicate"},
	}

	got := comic.DesiredIssues(c, true, map[string]bool{}, issues)
	require.Len(t, got, 1)
	assert.Equal(t, "First", got[0].Title)
}

// TestDesiredIssuesDatePrefersCoverDate: Date is CoverDate when present,
// falling back to StoreDate, matching IssueStatus.Date's own doc comment
// ("the issue's cover or store date").
func TestDesiredIssuesDatePrefersCoverDate(t *testing.T) {
	cover := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := time.Date(2025, 12, 15, 0, 0, 0, 0, time.UTC)
	c := newComic("batman", boolPtr(true))

	withBoth := comic.DesiredIssues(c, true, map[string]bool{}, []metadata.ComicIssue{
		{Number: "1", CoverDate: &cover, StoreDate: &store},
	})
	require.Len(t, withBoth, 1)
	require.NotNil(t, withBoth[0].Date)
	assert.True(t, withBoth[0].Date.Equal(cover))

	withStoreOnly := comic.DesiredIssues(c, true, map[string]bool{}, []metadata.ComicIssue{
		{Number: "2", StoreDate: &store},
	})
	require.Len(t, withStoreOnly, 1)
	require.NotNil(t, withStoreOnly[0].Date)
	assert.True(t, withStoreOnly[0].Date.Equal(store))

	withNeither := comic.DesiredIssues(c, true, map[string]bool{}, []metadata.ComicIssue{{Number: "3"}})
	require.Len(t, withNeither, 1)
	assert.Nil(t, withNeither[0].Date)
}

// TestDesiredIssuesSourceIDFollowsTheComicsSource: SourceID is the issue's
// id under the comic's own source key, never another provider's.
func TestDesiredIssuesSourceIDFollowsTheComicsSource(t *testing.T) {
	cv := newComic("batman", nil)
	got := comic.DesiredIssues(cv, false, map[string]bool{}, []metadata.ComicIssue{
		{IDs: metadata.ExternalIDs{metadata.KeyComicVine: "1001", "metron": "9"}, Number: "1"},
		{IDs: metadata.ExternalIDs{"metron": "10"}, Number: "2"},
	})
	require.Len(t, got, 2)
	assert.Equal(t, "1001", got[0].SourceID)
	assert.Empty(t, got[1].SourceID, "a Metron-only id is not a ComicVine issue id")

	md := newComic("one-piece", nil)
	md.Spec.Source = catalogv1alpha1.ComicSourceMangaDex
	got = comic.DesiredIssues(md, false, map[string]bool{}, []metadata.ComicIssue{
		{Number: "1"},
		{IDs: metadata.ExternalIDs{metadata.KeyComicVine: "3"}, Number: "2"},
	})
	require.Len(t, got, 2)
	assert.Empty(t, got[0].SourceID, "a MangaDex volume has no id")
	assert.Empty(t, got[1].SourceID, "a ComicVine id is not a MangaDex one")
}

func TestSourceKey(t *testing.T) {
	assert.Equal(t, "comicvine", comic.SourceKey(catalogv1alpha1.ComicSourceComicVine))
	assert.Equal(t, "mangadex", comic.SourceKey(catalogv1alpha1.ComicSourceMangaDex))
}
