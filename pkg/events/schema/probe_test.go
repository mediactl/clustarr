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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestProbeTaskAndRecordRoundTrip: a probe task travels as a bus message, its
// record as a KV value; both keep every field, and a seed (which answers no
// request) writes neither a request time nor a requested version.
func TestProbeTaskAndRecordRoundTrip(t *testing.T) {
	task := schema.ProbeTask{
		MediaFile: schema.Ref{Namespace: "media", Name: "heat-abc", UID: "u1"},
		Path:      "/data/media/movies/Heat (1995)/Heat (1995).mkv",
		ProbeHash: "h1", ProbeVersion: 3, Seq: 2, Lane: "high",
	}
	name, data, err := schema.Encode(task)
	require.NoError(t, err)
	assert.Equal(t, "import.ProbeTask.v1", name)
	var gotTask schema.ProbeTask
	require.NoError(t, schema.Decode(name, data, &gotTask))
	assert.Equal(t, task, gotTask)

	at := time.Date(2026, 10, 6, 20, 0, 0, 0, time.UTC)
	rec := schema.ProbeRecord{
		RecordHeader: schema.RecordHeader{
			MediaFile: task.MediaFile, Seq: 2, State: schema.ProbeProbed, RequestedAt: at,
		},
		Path: task.Path, ProbeHash: "h1", ProbeVersion: 3, RequestedVersion: 3, Lane: "high",
		ProbedAt:  at.Add(time.Second),
		MediaInfo: &commonv1.MediaInfo{Container: "mkv", VideoCodec: "hevc"},
		Source:    schema.ProbeSourceProbe, Prober: "import-worker-7c9",
	}
	b, err := json.Marshal(rec)
	require.NoError(t, err)
	var gotRec schema.ProbeRecord
	require.NoError(t, json.Unmarshal(b, &gotRec))
	assert.Equal(t, rec, gotRec)

	seed, err := json.Marshal(schema.ProbeRecord{
		RecordHeader: schema.RecordHeader{State: schema.ProbeProbed}, Source: schema.ProbeSourceImport,
	})
	require.NoError(t, err)
	assert.NotContains(t, string(seed), "requestedAt", "an unset time is omitted, not written as year 1")
	assert.NotContains(t, string(seed), "requestedVersion", "a seed answers no request")
}
