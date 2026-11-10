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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/events/schema/schematest"
)

// allPayloads is every versioned payload the bus carries -- the shared list
// in schematest, which app/catalog/history's resolver guard ranges over too.
func allPayloads() []schema.Payload { return schematest.Payloads() }

// wireNameExceptions are the schemas whose wire name names their struct by
// neither rule below, each with why it stays: a wire name is a contract
// with every envelope already stored, so a mismatch found after the fact is
// recorded here rather than renamed. The test checks each entry still
// applies, so a rename at the struct's next version drops its entry.
var wireNameExceptions = map[string]string{
	// Built 2026-10-22 (the MediUX image-sets automation design) as the
	// bucket's plural; renaming it is a wire break for clustarr-image-sets.
	"catalog.ImageSets.v1": "ImageSetRecord",
}

// TestSchemaNamesAreUniqueAndVersioned pins the header contract: payloads are
// versioned by Clustarr-Schema, so the value must name the struct and end in
// a version. A struct may carry the group as a prefix where the bare name
// would collide, as DownloadProgress and TranscodeProgress do; and a record
// -- a worker's answer in a Durable KV bucket, ADR-0019 (dc91000e, the
// agents-report-over-NATS design's §6.5 and §7) -- is named on the wire by
// what it records, "download.Transfer.v1" for TransferRecord and
// "catalog.ItemMetadata.v1" for ItemMetadataRecord, its Go type carrying
// Record as its suffix alone. (ProbeRecord and NFORecord, built later, say
// Record on the wire too, and pass as any struct does.)
func TestSchemaNamesAreUniqueAndVersioned(t *testing.T) {
	seen := map[string]bool{}
	excepted := map[string]bool{}
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
		got := reflect.TypeOf(p).Name()
		if want, ok := wireNameExceptions[name]; ok {
			excepted[name] = true
			if got != want {
				t.Errorf("schema %q is excepted for struct %q but names %q; move or drop the exception", name, want, got)
			} else if strings.HasSuffix(got, parts[1]) || strings.HasSuffix(got, parts[1]+"Record") {
				t.Errorf("schema %q now names its struct %q; drop its wireNameExceptions entry", name, got)
			}
			continue
		}
		if !strings.HasSuffix(got, parts[1]) && !strings.HasSuffix(got, parts[1]+"Record") {
			t.Errorf("schema %q does not name its struct %q", name, got)
		}
	}
	for name := range wireNameExceptions {
		if !excepted[name] {
			t.Errorf("wireNameExceptions names %q, which no payload in schematest.Payloads carries", name)
		}
	}
}

// TestEveryPayloadIsInTheSharedList holds schematest.Payloads to its claim
// of being every versioned payload the bus carries: each Schema() method
// declared in this package names a struct the list holds. ImageSetTask and
// ImageSetRecord (2026-10-22) were missing from it for three weeks, unseen
// by every guard that ranges over the list, which is what the list's own
// comment says cannot happen.
func TestEveryPayloadIsInTheSharedList(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse pkg/events/schema: %v", err)
	}
	listed := map[string]bool{}
	for _, p := range allPayloads() {
		listed[reflect.TypeOf(p).Name()] = true
	}
	var declared int
	for _, pkg := range pkgs {
		for file, f := range pkg.Files {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Name.Name != "Schema" || fd.Recv == nil || len(fd.Recv.List) != 1 || fd.Type.Params.NumFields() != 0 {
					continue
				}
				recv := fd.Recv.List[0].Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				ident, ok := recv.(*ast.Ident)
				if !ok {
					continue
				}
				declared++
				if !listed[ident.Name] {
					t.Errorf("%s declares a Schema() for %s, which schematest.Payloads does not list", filepath.Base(file), ident.Name)
				}
			}
		}
	}
	if declared < len(listed) {
		t.Errorf("found %d Schema() declarations for %d listed payloads; the parse missed some", declared, len(listed))
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

// TestEncodeDecodeRoundTrip pins the search task's wire name at its second
// version: since ADR-0019 (dc91000e, design §7.4) the task carries the
// item, the dispatch seq and everything the agent needs to decide -- the
// profile, protocols, scope -- so it reads no Kubernetes object, and the
// header moved to v2 with it.
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
		Item:        schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "media", Name: "the-expanse", UID: "u2"}},
		Seq:         3,
	}
	name, data, err := schema.Encode(in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if name != "catalog.SearchTask.v2" {
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
	var out schema.MetadataTask
	if err := schema.Decode(schema.SearchTask{}.Schema(), []byte("{}"), &out); err == nil {
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
// be serialised. (It encoded the v1 ImportTask until the ADR-0019 cold
// turkey deleted it; SearchTask is a task with a Ref and a v2 tail of
// optional fields.)
func TestOmitEmptyKeepsPayloadsSmall(t *testing.T) {
	_, data, err := schema.Encode(schema.SearchTask{
		MediaRef:  commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"},
		Reason:    schema.SearchReasonMissing,
		SearchRef: &schema.Ref{Namespace: "media", Name: "search-1"},
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(m) != 3 {
		t.Errorf("minimal SearchTask encoded %d fields: %s", len(m), data)
	}
	ref, _ := m["searchRef"].(map[string]any)
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
