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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestProbeRecordWireFormatIsUnchanged: embedding RecordHeader flat keeps every
// key W4.3 gave the probe record, with its type and meaning, and a record
// written before the fold decodes to the same values (loop spec §4.2, §4.12).
// The goldens are the pre-fold struct's encoding (d801c107^). It compares
// JSON objects, not bytes: the embedded header moves its keys to the front of
// the encoding, which no reader depends on. ADR-0019's Item (A1.4) is absent
// on a probe record and adds no key.
func TestProbeRecordWireFormatIsUnchanged(t *testing.T) {
	at := time.Date(2026, 10, 6, 20, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		golden string
		rec    schema.ProbeRecord
	}{
		{"probe-record.golden.json", schema.ProbeRecord{
			RecordHeader: schema.RecordHeader{
				MediaFile: schema.Ref{Namespace: "media", Name: "heat-abc", UID: "u1"},
				Seq:       2, State: schema.ProbeFailed, RequestedAt: at,
				Failure: "stuck in the demuxer", Transient: true,
			},
			Path: "/data/media/movies/Heat (1995)/Heat (1995).mkv", ProbeHash: "h1",
			ProbeVersion: 3, RequestedVersion: 3, Lane: "high", ProbedAt: at.Add(time.Second),
			MediaInfo: &commonv1.MediaInfo{Container: "mkv", VideoCodec: "hevc"},
			Abandoned: true, AbandonedCount: 2, Source: schema.ProbeSourceProbe, Prober: "import-worker-7c9",
		}},
		{"probe-record-seed.golden.json", schema.ProbeRecord{
			RecordHeader: schema.RecordHeader{State: schema.ProbeProbed}, Source: schema.ProbeSourceImport,
		}},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", tc.golden))
			require.NoError(t, err)
			got, err := json.Marshal(tc.rec)
			require.NoError(t, err)
			assert.JSONEq(t, string(want), string(got), "the probe record's keys changed")
			assert.NotContains(t, string(got), `"item"`, "an absent item adds no key")

			var back schema.ProbeRecord
			require.NoError(t, json.Unmarshal(want, &back))
			assert.Equal(t, tc.rec, back, "a record written before the fold decodes to the same values")
		})
	}
}

// TestEveryRecordHeaderKeyIsOptionalButTheProbes: the header's own keys are
// omitted when unset, so the only ones a zero header writes are the three the
// probe record always carried; and every record's failure has the probe's
// bound.
func TestEveryRecordHeaderKeyIsOptionalButTheProbes(t *testing.T) {
	b, err := json.Marshal(schema.RecordHeader{})
	require.NoError(t, err)
	assert.JSONEq(t, `{"mediaFile":{"name":""},"seq":0,"state":""}`, string(b),
		"a key the probe record never had is omitted when unset")
	h := &schema.RecordHeader{}
	assert.Same(t, h, h.Header())
	assert.Equal(t, schema.MaxRecordFailure, schema.MaxProbeFailure)
}

// TestBlockScopeOfIsInjective: a block's scope names exactly one item (ADR-0019
// §6.14). Kind, namespace, name and UID cannot hold a '/', so the scope
// splits back into the four it was built from, two items never share one,
// and none reads as the global scope.
func TestBlockScopeOfIsInjective(t *testing.T) {
	item := func(kind, ns, name, uid string) schema.ItemRef {
		return schema.ItemRef{Kind: kind, Ref: schema.Ref{Namespace: ns, Name: name, UID: uid}}
	}
	corpus := []schema.ItemRef{
		item("Movie", "media", "heat-1995", "4b0c5a7e-2f1d-4c8e-9a3b-6d7e8f901234"),
		item("Series", "media", "heat-1995", "4b0c5a7e-2f1d-4c8e-9a3b-6d7e8f901234"),
		item("Movie", "media-a", "b", "u1"),
		item("Movie", "media", "a-b", "u1"),
		item("Movie", "media", "a.b", "u1"),
		item("Movie", "media.a", "b", "u1"),
		item("Movie", "media", "ab", "u1"),
		item("Movie", "medi", "aab", "u1"),
		item("Movie", "media", "ab", "u"),
		item("Movie", "media", "abu", ""),
		item("Movie", "", "mediaab", "u1"),
		item("", "Movie", "media", "ab"),
		item("Episode", "media", "the-expanse-s01e01", "u2"),
		item("Episode", "media", "the-expanse-s01", "e01-u2"),
		item("", "", "", ""),
	}
	seen := map[string]schema.ItemRef{}
	for _, it := range corpus {
		scope := schema.BlockScopeOf(it)
		assert.NotEqual(t, schema.BlockScopeGlobal, scope, "%+v reads as the global scope", it)
		assert.Equal(t, []string{it.Kind, it.Namespace, it.Name, it.UID}, strings.Split(scope, "/"),
			"the scope %q does not split back into %+v", scope, it)
		if prev, dup := seen[scope]; dup {
			t.Errorf("%+v and %+v share the scope %q", prev, it, scope)
		}
		seen[scope] = it
	}
	assert.Equal(t, "Movie/media/heat-1995/u1", schema.BlockScopeOf(item("Movie", "media", "heat-1995", "u1")),
		"the documented form, which the release index stores: a change orphans every per-item block")
}
