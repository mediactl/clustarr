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
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestEveryPayloadRoundTrips encodes every payload the bus carries with every
// field set -- each to a value no other field has -- and decodes it back
// (ADR-0019 A1.4). A field a receiver cannot read back fails it: a "-" tag,
// two fields under one JSON key (encoding/json silently drops both), an
// unexported field a producer means to send, or an embedded record header
// whose key a record redeclares (encoding/json keeps the record's and drops
// the header's).
func TestEveryPayloadRoundTrips(t *testing.T) {
	// shadowed is every header field a record redeclares on purpose, so the
	// header's never reaches the wire. IndexerHealthRecord.Failures is the
	// indexer's running failure count; nothing sets the header's consecutive
	// count on that record (app/indexer/health keeps its own ladder).
	shadowed := map[string][]string{
		schema.IndexerHealthRecord{}.Schema(): {"RecordHeader", "Failures"},
	}
	for _, p := range allPayloads() {
		t.Run(p.Schema(), func(t *testing.T) {
			in := reflect.New(reflect.TypeOf(p))
			f := &filler{}
			f.fill(in.Elem(), nil)
			require.NotZero(t, f.n, "the payload has no field to fill")
			if path, ok := shadowed[p.Schema()]; ok {
				v := in.Elem()
				for _, name := range path {
					v = v.FieldByName(name)
				}
				v.SetZero()
			}

			name, data, err := schema.Encode(in.Elem().Interface().(schema.Payload))
			require.NoError(t, err)
			require.Equal(t, p.Schema(), name)

			out := reflect.New(reflect.TypeOf(p))
			require.NoError(t, schema.Decode(name, data, out.Interface().(schema.Payload)))
			assert.Equal(t, in.Elem().Interface(), out.Elem().Interface(),
				"a field did not survive the wire: %s", data)
		})
	}
}

// filler sets every exported field of a value to a distinct non-zero value,
// so a field lost or swapped on the wire shows in the comparison.
type filler struct{ n int }

var (
	timeType     = reflect.TypeFor[time.Time]()
	metaTimeType = reflect.TypeFor[metav1.Time]()
	rawType      = reflect.TypeFor[json.RawMessage]()
	quantityType = reflect.TypeFor[resource.Quantity]()
)

func (f *filler) next() int { f.n++; return f.n }

// fill sets v; path is the struct types it sits inside, so a recursive
// type is filled one level deep.
func (f *filler) fill(v reflect.Value, path []reflect.Type) {
	switch v.Type() {
	case timeType:
		v.Set(reflect.ValueOf(time.Date(2026, 10, 7, 12, 0, f.next(), 0, time.UTC)))
		return
	case metaTimeType:
		// metav1.Time decodes into time.Local (CLAUDE.md's gotcha), so it is
		// filled in Local for the comparison; the instant is what travels.
		v.Set(reflect.ValueOf(metav1.NewTime(time.Date(2026, 10, 7, 12, 0, f.next(), 0, time.UTC).Local())))
		return
	case rawType:
		v.Set(reflect.ValueOf(json.RawMessage(`{"n":` + strconv.Itoa(f.next()) + `}`)))
		return
	case quantityType:
		v.Set(reflect.ValueOf(resource.MustParse(strconv.Itoa(f.next()))))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("v" + strconv.Itoa(f.next()))
	case reflect.Bool:
		f.next()
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(1 + f.next()%100))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(1 + f.next()%100))
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		f.fill(p.Elem(), path)
		v.Set(p)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		f.fill(s.Index(0), path)
		v.Set(s)
	case reflect.Array:
		for i := range v.Len() {
			f.fill(v.Index(i), path)
		}
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		f.fill(k, path)
		e := reflect.New(v.Type().Elem()).Elem()
		f.fill(e, path)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Struct:
		if slices.Contains(path, v.Type()) {
			return
		}
		path = append(path, v.Type())
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				f.fill(v.Field(i), path)
			}
		}
	}
}
