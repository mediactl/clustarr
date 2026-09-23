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

package schema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// G1-6 closed catalogarr's ids-only gap by POPULATING SearchRequest.Text and
// SearchRequest.Year, both of which have carried "index.SearchRequest.v1"
// since M0 (pkg/events/schema/index.go, first added in f452f45) -- no new
// payload version, because schema.go's own contract only demands one for an
// INCOMPATIBLE change to an existing struct, and using an already-optional
// field for data it always documented ("the free-text query") is not one.
// This is a corrected, more specific claim than three places in this repo
// used to make (indexarr/search/query.go's old "CARRIED ITEM" comment,
// indexarr/search/doc.go and docs/superpowers/plans/2026-09-18-remaining-work.md's
// carried item), which all said the frozen payload had "no field" for a
// resolved title. It did.
func TestSearchRequestSchemaStringUnchanged(t *testing.T) {
	require.Equal(t, "index.SearchRequest.v1", SearchRequest{}.Schema())
	require.Equal(t, "index.SearchResponse.v1", SearchResponse{}.Schema())
}

// The one genuinely NEW field this task added -- SearchOutcome.QueryMode --
// must round-trip like any other optional field.
func TestSearchResponseQueryModeRoundTrips(t *testing.T) {
	resp := SearchResponse{Outcomes: []SearchOutcome{{
		IndexerRef: Ref{Name: "nzbgeek"},
		Status:     SearchOutcomeOK,
		Releases:   3,
		QueryMode:  SearchQueryModeText,
	}}}

	schemaName, data, err := Encode(resp)
	require.NoError(t, err)
	require.Equal(t, "index.SearchResponse.v1", schemaName)

	var decoded SearchResponse
	require.NoError(t, Decode(schemaName, data, &decoded))
	require.Equal(t, SearchQueryModeText, decoded.Outcomes[0].QueryMode)
}

// A message published before QueryMode existed carries no "queryMode" key at
// all. It must still decode -- as the zero value, not an error -- exactly as
// schema.go's package doc promises ("existing structs are never changed
// incompatibly").
func TestSearchResponseOldMessageWithNoQueryModeStillDecodes(t *testing.T) {
	raw := []byte(`{"releases":[],"outcomes":[{"indexerRef":{"name":"nzbgeek"},"status":"ok","releases":3}]}`)

	var resp SearchResponse
	require.NoError(t, Decode(SearchResponse{}.Schema(), raw, &resp))
	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, SearchOutcomeOK, resp.Outcomes[0].Status)
	require.Empty(t, resp.Outcomes[0].QueryMode,
		"an old message carries no query mode; it must decode as the zero value, not fail")
}

// The other direction: a consumer still compiled against the pre-G1-6 shape
// (no QueryMode field in its own struct at all) must still decode a message
// a G1-6 producer sends. encoding/json silently drops JSON keys the target
// struct has no field for, so this is the actual mechanism "an old consumer
// keeps working" rests on -- not a Decode-level compatibility switch, but
// json.Unmarshal's documented behaviour on an unknown field.
func TestAnOlderConsumerIgnoresTheNewQueryModeField(t *testing.T) {
	type oldSearchOutcome struct {
		IndexerRef Ref                 `json:"indexerRef"`
		Status     SearchOutcomeStatus `json:"status"`
		Releases   int32               `json:"releases"`
	}
	type oldSearchResponse struct {
		Outcomes []oldSearchOutcome `json:"outcomes,omitempty"`
	}

	resp := SearchResponse{Outcomes: []SearchOutcome{{
		IndexerRef: Ref{Name: "nzbgeek"},
		Status:     SearchOutcomeOK,
		Releases:   2,
		QueryMode:  SearchQueryModeID,
	}}}
	data, err := json.Marshal(resp)
	require.NoError(t, err)

	var old oldSearchResponse
	require.NoError(t, json.Unmarshal(data, &old))
	require.Len(t, old.Outcomes, 1)
	require.Equal(t, "nzbgeek", old.Outcomes[0].IndexerRef.Name)
	require.Equal(t, int32(2), old.Outcomes[0].Releases)
}
