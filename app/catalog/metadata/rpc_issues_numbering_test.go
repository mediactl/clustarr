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

package metadata

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// numberingStub is a MangaDex-shaped provider that is an IssueNumberer: it
// records the numbering each call asked for and answers chapters or
// volumes by it.
type numberingStub struct {
	stubComicProvider
	numberings *[]string
}

func (p numberingStub) IssuesNumbered(_ context.Context, volumeID string, numbering pkgmetadata.IssueNumbering) ([]pkgmetadata.ComicIssue, error) {
	*p.numberings = append(*p.numberings, "numbered:"+string(numbering))
	if numbering == pkgmetadata.IssueNumberingChapters {
		return []pkgmetadata.ComicIssue{{Number: "1"}, {Number: "2"}, {Number: "3"}}, nil
	}
	return p.Issues(context.Background(), volumeID)
}

var _ pkgmetadata.IssueNumberer = numberingStub{}

// A request naming a numbering reaches an IssueNumberer through
// IssuesNumbered, one naming none goes through Issues as before, and a
// provider that is no IssueNumberer is asked through Issues whatever the
// request says (2026-11-10 design D10; task L2).
func TestServeRPCLookupIssuesPassesTheNumberingToAnIssueNumberer(t *testing.T) {
	var calls, numberings []string
	reg := &pkgmetadata.Registry{Comics: []pkgmetadata.ComicProvider{
		numberingStub{
			stubComicProvider: stubComicProvider{calls: &calls, name: "mangadex", lookupBy: []string{"mangadex"}, issues: []pkgmetadata.ComicIssue{{Number: "41"}}},
			numberings:        &numberings,
		},
		stubComicProvider{calls: &calls, issues: []pkgmetadata.ComicIssue{{Number: "1"}}},
	}}
	bus := newTestBus(t)
	mustServeRPC(t, bus, reg)
	const manga = "801513ba-a712-498c-8f57-cae55b38cc92"

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindIssue, IDs: map[string]string{"mangadex": manga}, Numbering: string(pkgmetadata.IssueNumberingChapters)}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
	require.Empty(t, resp.Error)
	require.Equal(t, "mangadex", resp.Provider)
	require.Len(t, resp.Results, 3, "the chapters, not the one volume")
	var issue pkgmetadata.ComicIssue
	require.NoError(t, json.Unmarshal(resp.Results[2], &issue))
	require.Equal(t, "3", issue.Number)

	resp = schema.MetadataResponse{}
	req = schema.MetadataRequest{Kind: commonv1.MediaKindIssue, IDs: map[string]string{"mangadex": manga}}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
	require.Empty(t, resp.Error)
	require.Len(t, resp.Results, 1, "no numbering named: the provider's default, through Issues")

	resp = schema.MetadataResponse{}
	req = schema.MetadataRequest{Kind: commonv1.MediaKindIssue, IDs: map[string]string{"comicvine": "4050-12345"}, Numbering: string(pkgmetadata.IssueNumberingChapters)}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
	require.Empty(t, resp.Error)
	require.Equal(t, "comicvine", resp.Provider)
	require.Len(t, resp.Results, 1)

	require.Equal(t, []string{"numbered:chapters"}, numberings, "IssuesNumbered once, for the request that named a numbering")
	require.Equal(t, []string{"mangadex:" + manga, "comicvine:4050-12345"}, calls,
		"Issues for the unnumbered manga request and for the ComicVine provider, which is no IssueNumberer")
}
