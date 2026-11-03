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

package rowcache

import (
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// BenchmarkBuildDictionary measures what one kind's dictionary build
// allocates and retains: the manager builds four at once right after its
// initial sync, which is where its start-up heap spikes.
func BenchmarkBuildDictionary(b *testing.B) {
	samples := make([][]byte, 0, dictionarySamples)
	for i := range dictionarySamples {
		e := episode("media", fmt.Sprintf("series-%d", i%40), i)
		e.Status.CutoffReason = "Transcoded"
		js, err := json.Marshal(e)
		if err != nil {
			b.Fatal(err)
		}
		samples = append(samples, js)
	}
	gvk := schema.GroupVersionKind{Group: "catalog.clustarr.io", Version: "v1alpha1", Kind: "Episode"}
	b.ReportAllocs()
	for range b.N {
		c, err := newCodec("Episode", gvk, reflect.TypeFor[catalogv1alpha1.Episode]())
		if err != nil {
			b.Fatal(err)
		}
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		if err := c.buildDictionary(samples); err != nil {
			b.Fatal(err)
		}
		runtime.ReadMemStats(&after)
		runtime.GC()
		var settled runtime.MemStats
		runtime.ReadMemStats(&settled)
		b.ReportMetric(float64(after.TotalAlloc-before.TotalAlloc)/1e6, "MB-allocated/build")
		b.ReportMetric(float64(settled.HeapInuse-before.HeapInuse)/1e6, "MB-retained/build")
		b.ReportMetric(float64(len(c.dict))/1024, "dict-KiB")
	}
}
