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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestSubtitleEventIsV2AndNamesTheMediaFile: since F5.2 a subtitle event
// names the MediaFile and the dispatch's sequence, never a SubtitleRequest,
// and since the cold cutover (ADR-0019 A9.1) a v1 event is not read as one.
func TestSubtitleEventIsV2AndNamesTheMediaFile(t *testing.T) {
	assert.Equal(t, "subtitle.SubtitleEvent.v2", schema.SubtitleEvent{}.Schema())
	_, data, err := schema.Encode(schema.SubtitleEvent{
		MediaFile: schema.Ref{Namespace: "m", Name: "f", UID: "u"}, Seq: 7, Action: "downloaded", LangKey: "en",
	})
	require.NoError(t, err)
	assert.Contains(t, string(data), `"mediaFile":{"namespace":"m","name":"f","uid":"u"}`)
	assert.Contains(t, string(data), `"seq":7`)
	assert.NotContains(t, string(data), "requestRef")

	var out schema.SubtitleEvent
	assert.Error(t, schema.Decode("subtitle.SubtitleEvent.v1",
		[]byte(`{"requestRef":{"namespace":"m","name":"req-1"},"action":"downloaded","langKey":"en"}`), &out),
		"a v1 event names a SubtitleRequest; nothing reads it as a v2 one")
}

// TestSubtitleRecordKeepsTheHeaderFlat: the record header's keys sit beside
// the subtitle record's own, as pkg/records reads every records-bucket value.
func TestSubtitleRecordKeepsTheHeaderFlat(t *testing.T) {
	var r schema.SubtitleRecord
	r.Schema, r.Seq, r.State, r.Sub = schema.SubtitleRecordSchema, 3, "answered", "de"
	r.Verdict = schema.SubtitleVerdictKept
	b, err := json.Marshal(r)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"schema":"records.subtitle.v1"`)
	assert.Contains(t, string(b), `"sub":"de"`)
	assert.Contains(t, string(b), `"verdict":"kept"`)
	assert.NotContains(t, string(b), `"RecordHeader"`)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b, &top))
	for _, k := range []string{"schema", "mediaFile", "sub", "seq", "state", "verdict"} {
		assert.Contains(t, top, k, "the header's %q is not a top-level key: %s", k, b)
	}

	var back schema.SubtitleRecord
	require.NoError(t, json.Unmarshal(b, &back))
	assert.Equal(t, r, back)
	assert.Equal(t, "de", back.Header().Sub, "pkg/records reaches the header through Header()")
}
