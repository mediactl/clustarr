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
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/pkg/metadata"
)

func day(d int) *time.Time {
	t := now.AddDate(0, 0, d)
	return &t
}

// The add-time monitor mode, Mylar's and Sonarr's (2026-11-10 design D10):
// every mode against an issue out, one to come and one undated, for a
// volume still running and one ended.
func TestInitialIssueMonitored(t *testing.T) {
	out := comic.IssueCandidate{Number: "1", Released: day(-30)}
	toCome := comic.IssueCandidate{Number: "2", Released: day(30)}
	undated := comic.IssueCandidate{Number: "3"}
	continuing, ended := catalogv1alpha1.ComicRunStatusContinuing, catalogv1alpha1.ComicRunStatusEnded
	cases := []struct {
		name string
		mode catalogv1alpha1.ComicMonitorMode
		iss  comic.IssueCandidate
		run  catalogv1alpha1.ComicRunStatus
		want bool
	}{
		{"all: out", catalogv1alpha1.ComicMonitorAll, out, continuing, true},
		{"all: to come", catalogv1alpha1.ComicMonitorAll, toCome, continuing, true},
		{"unset reads as all", "", toCome, ended, true},
		{"future: out is not wanted", catalogv1alpha1.ComicMonitorFuture, out, continuing, false},
		{"future: to come is wanted", catalogv1alpha1.ComicMonitorFuture, toCome, continuing, true},
		{"future: to come of an ended run is not", catalogv1alpha1.ComicMonitorFuture, toCome, ended, false},
		{"future: undated counts as out", catalogv1alpha1.ComicMonitorFuture, undated, continuing, false},
		{"missing: out is wanted", catalogv1alpha1.ComicMonitorMissing, out, continuing, true},
		{"missing: to come is not", catalogv1alpha1.ComicMonitorMissing, toCome, continuing, false},
		{"missing: undated counts as out", catalogv1alpha1.ComicMonitorMissing, undated, continuing, true},
		{"existing: nothing has a file at add", catalogv1alpha1.ComicMonitorExisting, out, continuing, false},
		{"none", catalogv1alpha1.ComicMonitorNone, out, continuing, false},
		{"an unknown mode monitors nothing", "weekly", out, continuing, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, comic.InitialIssueMonitored(c.mode, c.iss, c.run, now))
		})
	}
}

// ReleaseDate is the store date, else the cover date: a US comic's cover
// date runs ahead of the day it reaches the shops.
func TestReleaseDateIsTheStoreDateElseTheCoverDate(t *testing.T) {
	store, cover := day(-60), day(0)
	assert.Equal(t, store, comic.ReleaseDate(metadata.ComicIssue{StoreDate: store, CoverDate: cover}))
	assert.Equal(t, cover, comic.ReleaseDate(metadata.ComicIssue{CoverDate: cover}))
	assert.Nil(t, comic.ReleaseDate(metadata.ComicIssue{}))
}

// The first fan-out applies spec.addOptions.monitor to every new issue;
// unmonitoredIssues still outranks it, and an existing Issue is left alone.
func TestDesiredIssuesAppliesTheAddTimeMonitorMode(t *testing.T) {
	c := newComic("saga", boolPtr(true), "2")
	c.Spec.AddOptions.Monitor = catalogv1alpha1.ComicMonitorFuture
	c.Status.Metadata = &catalogv1alpha1.ComicMetadata{Status: catalogv1alpha1.ComicRunStatusContinuing}
	issues := []metadata.ComicIssue{
		{Number: "1", StoreDate: day(-100)},
		{Number: "2", StoreDate: day(10)},
		{Number: "3", StoreDate: day(20)},
		{Number: "4", CoverDate: day(60), StoreDate: day(5)},
	}
	got := comic.DesiredIssues(c, false, map[string]bool{comic.IssueName("saga", "3"): true}, issues, now)
	require.Len(t, got, 4)
	byNumber := map[string]comic.DesiredIssue{}
	for _, d := range got {
		byNumber[d.Number] = d
	}
	require.NotNil(t, byNumber["1"].Monitored)
	assert.False(t, *byNumber["1"].Monitored, "future: an issue already out is not wanted")
	require.NotNil(t, byNumber["2"].Monitored)
	assert.False(t, *byNumber["2"].Monitored, "spec.unmonitoredIssues outranks the mode")
	assert.Nil(t, byNumber["3"].Monitored, "an existing Issue is the user's")
	require.NotNil(t, byNumber["4"].Monitored)
	assert.True(t, *byNumber["4"].Monitored, "future: an issue reaching the shops in five days is wanted, whatever its cover date says")

	// Once applied, a new issue follows monitorNewIssues, not the mode.
	c.Spec.MonitorNewIssues = boolPtr(false)
	later := comic.DesiredIssues(c, true, map[string]bool{}, []metadata.ComicIssue{{Number: "5", StoreDate: day(40)}}, now)
	require.Len(t, later, 1)
	require.NotNil(t, later[0].Monitored)
	assert.False(t, *later[0].Monitored)
}

// Under specialVersion volumeAsIssue the Issues are numbered by volume
// (D10, Mylar's rule): a number or title spelling a volume gives the
// number; a plain number passes through; two spellings of one volume are
// one Issue. Every other special version keeps the provider's numbers.
func TestDesiredIssuesNumbersByVolumeUnderVolumeAsIssue(t *testing.T) {
	issues := []metadata.ComicIssue{
		{Number: "1", Title: "Volume 1"},
		{Number: "Vol. 2", Title: "The Second Collection"},
		{Number: "TPB", Title: "Chainsaw Man, Vol. 3"},
		{Number: "Vol. 03", Title: "a duplicate spelling of the third"},
		{Number: "HC", Title: "Deluxe Edition"},
	}
	c := newComic("csm", nil)
	c.Spec.SpecialVersion = catalogv1alpha1.ComicSpecialVersionVolumeAsIssue
	got := comic.DesiredIssues(c, true, map[string]bool{}, issues, now)
	var numbers, names []string
	for _, d := range got {
		numbers = append(numbers, d.Number)
		names = append(names, d.Name)
	}
	assert.Equal(t, []string{"1", "2", "3", "HC"}, numbers)
	assert.Equal(t, []string{comic.IssueName("csm", "1"), comic.IssueName("csm", "2"), comic.IssueName("csm", "3"), comic.IssueName("csm", "HC")}, names,
		"the name is made from the volume number, so a renumbering is a new Issue")

	normal := newComic("csm", nil)
	normal.Spec.SpecialVersion = catalogv1alpha1.ComicSpecialVersionTPB
	plain := comic.DesiredIssues(normal, true, map[string]bool{}, issues, now)
	numbers = nil
	for _, d := range plain {
		numbers = append(numbers, d.Number)
	}
	assert.Equal(t, []string{"1", "Vol. 2", "TPB", "Vol. 03", "HC"}, numbers, "a TPB run keeps the provider's numbers")
}

// NextPullDate is the earliest store date (else cover date) still to come
// among every Issue, monitored or not; nil when none is.
func TestNextPullDate(t *testing.T) {
	mt := func(d int) *metav1.Time { t := metav1.NewTime(now.AddDate(0, 0, d)); return &t }
	issue := func(store, cover *metav1.Time, monitored bool) catalogv1alpha1.Issue {
		return catalogv1alpha1.Issue{
			Spec:   catalogv1alpha1.IssueSpec{Monitored: boolPtr(monitored)},
			Status: catalogv1alpha1.IssueStatus{StoreDate: store, Date: cover},
		}
	}
	issues := []catalogv1alpha1.Issue{
		issue(mt(-30), mt(0), true),
		issue(mt(14), mt(60), true),
		// The store date is what the pull list reads, not the cover date,
		// which runs two months ahead.
		issue(mt(7), mt(70), false),
		// No store date: the cover date stands in.
		issue(nil, mt(21), true),
		issue(nil, nil, true),
	}
	got := comic.NextPullDate(issues, now)
	require.NotNil(t, got)
	assert.Equal(t, mt(7).Time, got.Time, "the unmonitored issue's store date is still the next pull")

	assert.Nil(t, comic.NextPullDate(nil, now))
	assert.Nil(t, comic.NextPullDate([]catalogv1alpha1.Issue{issue(mt(-1), mt(30), true)}, now),
		"an issue in the shops is on no pull list, whatever its cover date")
	exact := comic.NextPullDate([]catalogv1alpha1.Issue{issue(mt(0), nil, true)}, now)
	require.NotNil(t, exact)
	assert.Equal(t, now, exact.Time, "an issue out today is still today's pull")
}
