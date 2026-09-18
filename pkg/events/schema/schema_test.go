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
	"reflect"
	"strings"
	"testing"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// allPayloads is every versioned payload the bus carries.
func allPayloads() []schema.Payload {
	return []schema.Payload{
		schema.ItemEvent{},
		schema.ReleaseEvent{},
		schema.MediaFileEvent{},
		schema.ImportListSynced{},
		schema.SearchTask{},
		schema.GrabTask{},
		schema.ImportTask{},
		schema.MetadataTask{},
		schema.ImportListTask{},
		schema.WantedScan{},
		schema.MetadataRequest{},
		schema.MetadataResponse{},
		schema.Release{},
		schema.IndexerEvent{},
		schema.RssTask{},
		schema.DefinitionsSync{},
		schema.SearchRequest{},
		schema.SearchResponse{},
		schema.DownloadRequest{},
		schema.DownloadResponse{},
		schema.QueryRequest{},
		schema.QueryResponse{},
		schema.DownloadEvent{},
		schema.DownloadProgress{},
		schema.JobEvent{},
		schema.TranscodeProgress{},
		schema.SubtitleEvent{},
		schema.FetchTask{},
		schema.ScanTask{},
	}
}

// TestSchemaNamesAreUniqueAndVersioned pins the header contract: payloads are
// versioned by Clustarr-Schema, so the value must name the struct and end in
// a version.
func TestSchemaNamesAreUniqueAndVersioned(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range allPayloads() {
		name := p.Schema()
		if seen[name] {
			t.Errorf("duplicate schema name %q", name)
		}
		seen[name] = true
		parts := strings.Split(name, ".")
		if len(parts) != 3 {
			t.Errorf("schema %q is not <group>.<Struct>.<version>", name)
			continue
		}
		if !strings.HasPrefix(parts[2], "v") {
			t.Errorf("schema %q does not end in a version", name)
		}
		// The struct may carry the group as a prefix where the bare name
		// would collide, as DownloadProgress and TranscodeProgress do.
		if got := reflect.TypeOf(p).Name(); !strings.HasSuffix(got, parts[1]) {
			t.Errorf("schema %q does not name its struct %q", name, got)
		}
	}
}

// TestNoFloatFields pins the project-wide rule: fractional values are carried
// as scaled integers or resource.Quantity, never as floats.
func TestNoFloatFields(t *testing.T) {
	for _, p := range allPayloads() {
		walkFields(t, reflect.TypeOf(p), p.Schema(), map[reflect.Type]bool{})
	}
}

func walkFields(t *testing.T, typ reflect.Type, path string, seen map[reflect.Type]bool) {
	t.Helper()
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice ||
		typ.Kind() == reflect.Array {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Float32, reflect.Float64:
		t.Errorf("%s is a float; use a scaled integer instead", path)
		return
	case reflect.Struct:
	default:
		return
	}
	if seen[typ] {
		return
	}
	seen[typ] = true
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		walkFields(t, f.Type, path+"."+f.Name, seen)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	in := schema.SearchTask{
		MediaRef: commonv1.MediaRef{
			Kind: commonv1.MediaKindEpisode,
			Name: "the-expanse",
			Keys: []string{"s01e01", "s01e02"},
		},
		Reason:      schema.SearchReasonCutoffUnmet,
		SearchRef:   &schema.Ref{Namespace: "media", Name: "search-1", UID: "u1"},
		UserInvoked: true,
	}
	name, data, err := schema.Encode(in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if name != "catalog.SearchTask.v1" {
		t.Errorf("schema = %q", name)
	}

	var out schema.SearchTask
	if err := schema.Decode(name, data, &out); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip changed the payload:\n got %+v\nwant %+v", out, in)
	}
}

func TestDecodeRejectsMismatchedSchema(t *testing.T) {
	var out schema.GrabTask
	if err := schema.Decode("catalog.SearchTask.v1", []byte("{}"), &out); err == nil {
		t.Fatal("Decode accepted a payload declared as another schema")
	}
	// An absent header is tolerated, for messages published before the
	// header was mandatory.
	if err := schema.Decode("", []byte("{}"), &out); err != nil {
		t.Errorf("Decode with no schema header: %v", err)
	}
}

// TestOmitEmptyKeepsPayloadsSmall checks the JSON shape of a minimal task:
// work payloads ride in a 1 GiB work-queue stream, so unset fields must not
// be serialised.
func TestOmitEmptyKeepsPayloadsSmall(t *testing.T) {
	_, data, err := schema.Encode(schema.ImportTask{
		DownloadRef: schema.Ref{Namespace: "media", Name: "dl-1"},
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(m) != 1 {
		t.Errorf("minimal ImportTask encoded %d fields: %s", len(m), data)
	}
	ref, _ := m["downloadRef"].(map[string]any)
	if _, ok := ref["uid"]; ok {
		t.Errorf("an unset uid was serialised: %s", data)
	}
}

func TestRefKey(t *testing.T) {
	if got := (schema.Ref{Namespace: "media", Name: "movie-1"}).Key(); got != "media/movie-1" {
		t.Errorf("Key = %q", got)
	}
	if got := (schema.Ref{Name: "cluster-wide"}).Key(); got != "cluster-wide" {
		t.Errorf("Key = %q", got)
	}
}

func TestProgressUsesScaledIntegers(t *testing.T) {
	p := schema.DownloadProgress{
		DownloadRef:  schema.Ref{Name: "dl-1"},
		PercentMilli: 42_500, // 42.5 %
		RatioMilli:   1_750,  // 1.75
		At:           time.Now().UTC(),
	}
	_, data, err := schema.Encode(p)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(string(data), `"percentMilli":42500`) {
		t.Errorf("percentMilli was not encoded as an integer: %s", data)
	}
}
