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

package schema_test

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestMarkersTaskRoundTrip: the markers task carries everything the marker
// worker asks TheIntroDB with -- the file, the sequence its record is fenced
// by, the probe the answer is for and the query -- so the worker reads no
// Kubernetes object (loop spec §4.12). It replaced SegmentsResult (F3.4); a
// v1 task ({"mediaFile": <name>}) is not decoded as one, which is how the
// worker knows to ack it unanswered.
func TestMarkersTaskRoundTrip(t *testing.T) {
	in := schema.MarkersTask{
		File: schema.Ref{Namespace: "media", Name: "the-expanse-s01e01-abc", UID: "u1"},
		Seq:  4,
		Inputs: schema.MarkersInputs{
			ProbeHash: "h1", DurationMs: 2_820_000,
			Query: schema.MarkersQuery{IDs: map[string]string{"tvdb": "280619"}, Season: 1, Episode: 1},
		},
		SeriesKey: "media/the-expanse",
	}
	name, data, err := schema.Encode(in)
	require.NoError(t, err)
	assert.Equal(t, "catalog.MarkersTask.v2", name)

	var keys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &keys))
	assert.ElementsMatch(t, []string{"file", "seq", "inputs", "seriesKey"}, slices.Collect(maps.Keys(keys)))
	assert.JSONEq(t, `{"probeHash":"h1","durationMs":2820000,"query":{"ids":{"tvdb":"280619"},"season":1,"episode":1}}`,
		string(keys["inputs"]))

	var out schema.MarkersTask
	require.NoError(t, schema.Decode(name, data, &out))
	assert.Equal(t, in, out)

	assert.Error(t, schema.Decode("catalog.MarkersTask.v1", []byte(`{"mediaFile":"heat-abc"}`), &out),
		"a v1 task is never read as a v2 one")
}
