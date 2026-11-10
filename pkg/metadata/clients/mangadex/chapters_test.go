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

package mangadex_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
)

func numbers(issues []metadata.ComicIssue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.Number)
	}
	return out
}

// A manga Comic numbers its Issues by chapter (2026-11-10 design D10): the
// aggregate's every numbered chapter, the uncollected "none" bucket's
// included, in chapter order, each with its MangaDex chapter id -- where the
// default numbering lists the collected volumes alone.
func TestIssuesNumberedByChaptersListsEveryChapterOfTheAggregate(t *testing.T) {
	srv, seen := serve(t, map[string]string{"/manga/" + berserk + "/aggregate": "aggregate_" + berserk + ".json"})
	c := newClient(srv)

	var numberer metadata.IssueNumberer = c
	issues, err := numberer.IssuesNumbered(context.Background(), berserk, metadata.IssueNumberingChapters)

	require.NoError(t, err)
	require.Len(t, *seen, 1, "one aggregate request, as for volumes")
	require.Equal(t, []string{"2", "3", "4", "5", "356.2", "357", "363", "364", "385", "386"}, numbers(issues),
		`chapters 385 and 386 are uncollected ("none") and still issues; 356.2 sorts as a number`)
	require.Equal(t, metadata.ExternalIDs{extid.KeyMangaDex: "38310cd3-d890-4d0e-bc21-d87b37fae891"}, issues[0].IDs,
		"the chapter's own MangaDex id, as a ComicVine issue carries its id")
	require.Empty(t, issues[0].Title, "the aggregate names no chapter")
}

// The volume numbering is the default and is unchanged: Issues, an empty
// numbering and "volumes" list the same collected volumes.
func TestIssuesNumberedByVolumesIsTheDefault(t *testing.T) {
	srv, _ := serve(t, map[string]string{"/manga/" + berserk + "/aggregate": "aggregate_" + berserk + ".json"})
	c := newClient(srv)

	byDefault, err := c.Issues(context.Background(), berserk)
	require.NoError(t, err)
	byEmpty, err := c.IssuesNumbered(context.Background(), berserk, metadata.IssueNumberingDefault)
	require.NoError(t, err)
	byVolumes, err := c.IssuesNumbered(context.Background(), berserk, metadata.IssueNumberingVolumes)
	require.NoError(t, err)

	require.Equal(t, []string{"1", "2", "40", "41"}, numbers(byDefault))
	require.Equal(t, byDefault, byEmpty)
	require.Equal(t, byDefault, byVolumes)
	for _, i := range byDefault {
		require.Empty(t, i.IDs, "a volume has no id of its own on MangaDex")
	}
}

// A chapter two volumes list is one issue, the lower volume's; a chapter
// without a number is none; chapters serialized as an empty array decode.
func TestIssuesNumberedByChaptersDedupesAndSkipsUnnumbered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"ok","volumes":{
			"none":{"volume":"none","count":1,"chapters":{"none":{"chapter":"none","id":"oneshot","count":1}}},
			"2":{"volume":"2","count":1,"chapters":{"7":{"chapter":"7","id":"seven-again","count":1}}},
			"1":{"volume":"1","count":2,"chapters":{"7":{"chapter":"7","id":"seven","count":1},"6":{"chapter":"6","id":"six","count":1}}},
			"3":{"volume":"3","count":0,"chapters":[]}
		}}`))
	}))
	t.Cleanup(srv.Close)

	issues, err := newClient(srv).IssuesNumbered(context.Background(), berserk, metadata.IssueNumberingChapters)

	require.NoError(t, err)
	require.Equal(t, []string{"6", "7"}, numbers(issues))
	require.Equal(t, "seven", issues[1].IDs[extid.KeyMangaDex], "volume 1's listing of chapter 7, not volume 2's")
}

func TestIssuesNumberedRefusesAnUnknownNumbering(t *testing.T) {
	srv, seen := serve(t, nil)

	_, err := newClient(srv).IssuesNumbered(context.Background(), berserk, metadata.IssueNumbering("pages"))

	require.ErrorIs(t, err, metadata.ErrUnsupported)
	require.Empty(t, *seen, "refused before any request")
}
