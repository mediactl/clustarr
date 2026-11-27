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

package usenet

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// unlist removes the segments with the given message-IDs from an NZB
// buildNZB wrote, as an indexer's NZB that holds only part of a post lacks
// them: the articles stay on the server, and nothing names them.
func unlist(t *testing.T, nzb []byte, ids ...string) []byte {
	t.Helper()
	for _, id := range ids {
		re := regexp.MustCompile(`<segment [^>]*>` + regexp.QuoteMeta(id) + `</segment>`)
		require.Len(t, re.FindAll(nzb, -1), 1, "segment %s", id)
		nzb = re.ReplaceAll(nzb, nil)
	}
	return nzb
}

func sixParts() [][]byte {
	parts := make([][]byte, 6)
	for i := range parts {
		parts[i] = partPayload(byte(i+1), 400)
	}
	return parts
}

// nzbgeek's NZBs for two episodes of Fena: Pirate Princess listed 1,735 of
// a file's 2,087 articles and 1,248 of 2,079 (talos, 2026-11-17): the
// numbers an NZB skips, and the ones past its last that the yEnc subject
// still counts, are articles of the post it does not list.
func TestAnNZBCountsTheArticlesItDoesNotList(t *testing.T) {
	whole := buildNZB(t, nil, "Short", []fileSpec{
		{name: "movie.mkv", parts: sixParts()},
		{name: "movie.par2", parts: [][]byte{partPayload(9, 100)}},
	})
	full, err := parseNZB(whole, 0)
	require.NoError(t, err)
	require.Zero(t, full.Unlisted)
	require.Zero(t, full.UnlistedBytes)

	t.Run("numbers skipped below the highest", func(t *testing.T) {
		job, err := parseNZB(unlist(t, whole, "f0-p1@clustarr.test", "f0-p3@clustarr.test"), 0)
		require.NoError(t, err)
		require.Equal(t, 2, job.Files[0].Unlisted)
		require.Equal(t, 2, job.Unlisted)
		require.Equal(t, full.TotalSegments, job.TotalSegments, "the post's articles, listed or not")
		require.Positive(t, job.UnlistedBytes)
		require.InDelta(t, float64(full.TotalBytes), float64(job.TotalBytes), float64(full.TotalBytes)/50)
	})
	t.Run("the last ones, which the yEnc subject still counts", func(t *testing.T) {
		job, err := parseNZB(unlist(t, whole, "f0-p4@clustarr.test", "f0-p5@clustarr.test"), 0)
		require.NoError(t, err)
		require.Equal(t, 4, job.Files[0].Highest)
		require.Equal(t, 2, job.Files[0].Unlisted)
	})
	t.Run("a par2 file's are not content bytes", func(t *testing.T) {
		short := buildNZB(t, nil, "Short", []fileSpec{
			{name: "movie.mkv", parts: sixParts()},
			{name: "movie.vol000+02.par2", parts: [][]byte{partPayload(9, 100), partPayload(8, 100), partPayload(7, 100)}},
		})
		job, err := parseNZB(unlist(t, short, "f1-p1@clustarr.test"), 0)
		require.NoError(t, err)
		require.Equal(t, 1, job.Unlisted)
		require.Zero(t, job.UnlistedBytes, "missing recovery data is not missing content (SABnzbd's bytes_missing)")
	})
	t.Run("a subject's file counter is not a part count", func(t *testing.T) {
		// "[1/9] name" numbers the post's files; only "yEnc (1/6)" counts parts.
		plain := bytes.ReplaceAll(whole, []byte(` yEnc (1/6)`), nil)
		require.NotEqual(t, whole, plain)
		job, err := parseNZB(plain, 0)
		require.NoError(t, err)
		require.Zero(t, job.Unlisted)
	})
}

// Bare content an NZB lists only part of was published whole-looking, its
// unlisted parts holes: every article asked for had arrived, so nothing was
// missing and nothing ran par2. It is judged before a byte moves.
func TestBareContentAnNZBListsPartOfIsNotFetched(t *testing.T) {
	srv := newStubServer(t)
	nzb := unlist(t, buildNZB(t, srv, "Short", []fileSpec{{name: "movie.mkv", parts: sixParts()}}),
		"f0-p2@clustarr.test", "f0-p4@clustarr.test")

	c, _, dataDir := newTestClient(t, Config{
		Providers:    []Provider{srv.provider("solo", 2, 1)},
		HealthAction: downloadv1alpha1.HealthActionDelete,
	})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Short", Payload: nzb, Category: "movies"})
	require.NoError(t, err)

	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusFailed, it.Status, "message: %s", it.Message)
	require.Equal(t, commonv1alpha1.DownloadFailureMissingArticles, it.FailureReason)
	require.Contains(t, it.Message, "2 of 6 articles missing")
	require.Contains(t, it.Message, "the NZB does not list 2 of the post's articles")
	require.NotNil(t, it.Health)
	require.Equal(t, int32(2), it.Health.FailedArticles)
	require.Equal(t, int32(6), it.Health.TotalArticles)
	require.Zero(t, srv.askedTotal(), "a post known short from its NZB is not fetched to find that out")
	_, err = os.Stat(filepath.Join(dataDir, "movies", "Short"))
	require.True(t, os.IsNotExist(err), "nothing is published")
}

// Under healthAction pause the same post is held for an operator.
func TestAnNZBListingPartOfAPostPausesUnderHealthActionPause(t *testing.T) {
	srv := newStubServer(t)
	nzb := unlist(t, buildNZB(t, srv, "Short", []fileSpec{{name: "movie.mkv", parts: sixParts()}}), "f0-p2@clustarr.test")
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Short", Payload: nzb, Category: "movies"})
	require.NoError(t, err)
	it := waitForHealthPause(t, c, id)
	require.Contains(t, it.Message, "1 of 6 articles missing, with no par2 to repair them")
	require.Zero(t, srv.askedTotal())
}

// A post with recovery data to spare is fetched, and par2 runs on it: the
// quick check that skips par2 holds only when nothing is missing.
func TestPar2RunsOnAPostItsNZBListsPartOf(t *testing.T) {
	srv := newStubServer(t)
	vol := make([][]byte, 4)
	for i := range vol {
		vol[i] = partPayload(byte(20+i), 400)
	}
	nzb := unlist(t, buildNZB(t, srv, "Short.Set", []fileSpec{
		{name: "movie.mkv", parts: sixParts()},
		{name: "movie.par2", parts: [][]byte{partPayload(9, 100)}},
		{name: "movie.vol000+04.par2", parts: vol},
	}), "f0-p2@clustarr.test")

	ran := 0
	fake := fakeRepairer{available: true, repair: func(context.Context, string, string) (Par2Result, error) {
		ran++
		return Par2Result{}, fmt.Errorf("the fake repairs nothing")
	}}
	c, _, dataDir := newTestClient(t, Config{
		Providers: []Provider{srv.provider("solo", 2, 1)}, Repairer: fake, PostProcess: PostProcess{Par2: true},
		HealthAction: downloadv1alpha1.HealthActionDelete,
	})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Short.Set", Payload: nzb, Category: "movies"})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, 1, ran, "par2 judges a file with an unlisted article: %s", it.Message)
	require.Equal(t, download.StatusFailed, it.Status, "an unrepaired set is not published: %s", it.Message)
	_, err = os.Stat(filepath.Join(dataDir, "movies", "Short.Set"))
	require.True(t, os.IsNotExist(err))
}
