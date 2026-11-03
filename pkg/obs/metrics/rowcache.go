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

package metrics

// Row cache telemetry (pkg/k8s/rowcache): the manager's informers for its
// large kinds hold each object as one compressed row instead of a decoded
// Go object.
var (
	// RowCacheRows is how many objects a kind's informer holds as rows.
	RowCacheRows = newGaugeVec("clustarr_rowcache_rows",
		"Objects the row cache holds as compressed rows, by kind.", "kind")
	// RowCacheBytes is the compressed bytes a kind's rows take.
	RowCacheBytes = newGaugeVec("clustarr_rowcache_bytes",
		"Compressed bytes the row cache holds, by kind.", "kind")
	// RowCacheObjectBytes is the serialized size of the same objects before
	// compression, so bytes over object_bytes is the compression ratio.
	RowCacheObjectBytes = newGaugeVec("clustarr_rowcache_object_bytes",
		"Serialized bytes of the objects the row cache holds, before compression, by kind.", "kind")
	// RowCacheDictionaryBytes is the size of the dictionary a kind's rows
	// are compressed with; 0 until one is built from the initial list.
	RowCacheDictionaryBytes = newGaugeVec("clustarr_rowcache_dictionary_bytes",
		"Size of the compression dictionary a kind's rows use, by kind.", "kind")
	// RowCacheDecodesTotal counts rows decoded back into objects, by kind
	// and reason: read (a Get or List), event (a handler notification),
	// index (an index function run on a row stored before it was
	// registered), sample (dictionary training).
	RowCacheDecodesTotal = newCounterVec("clustarr_rowcache_decodes_total",
		"Rows the row cache decoded back into objects, by kind and reason.", "kind", "reason")
)
