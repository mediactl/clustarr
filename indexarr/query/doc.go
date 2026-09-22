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

// Package query serves clustarr.rpc.indexarr.query: a direct read of the
// local release index.
//
// # Local index only
//
// No HTTP client is constructed, no Indexer is read, no bus call is made and
// there is no fan-out. Ruling R4 is deliberate about this: the verb has zero
// callers today, and a cold-index fallback, a quality filter the store cannot
// serve or an offset the store has no concept of would all be API surface
// nothing will ever exercise.
//
// # Text is normalised, never escaped
//
// QueryRequest.Text is attacker-controlled -- it arrives from user input and
// from third-party indexer titles. Two jobs meet here and only one belongs to
// this package.
//
// Escaping is the store's. pkg/relindex's matchExpr quotes every token into
// an FTS5 string, so operators, unbalanced quotes and bare AND/OR/NEAR are
// literals. Escaping again here would compose two escapers into a query that
// matches nothing, so a hostile string is a normal -- probably empty --
// result, never an error and never a panic.
//
// Normalising is the caller's, and pkg/relindex says so outright: it stores
// the Release.TitleNorm it is given and does not normalise Query.Text, so the
// two must go through ONE function or nothing matches. The RSS worker fills
// that column with release.CleanTitle, so this verb does too. The failure
// mode if they ever diverge is silent -- an index that answers nothing rather
// than an error -- which is why filters_test.go reads the worker's source and
// fails here when it changes.
//
// # The filter vocabulary is closed
//
// category, indexer, protocol, since. An unknown key is an ERROR rather than
// a silent no-op: silently dropping a filter returns MORE data than the
// caller asked for and the caller cannot tell. schema.QueryRequest's own doc
// names "quality" as an example filter; relindex.Query has no quality column,
// so it is rejected by name and the gap is a carried item.
//
// # Paging is over-fetch plus slice
//
// QueryRequest.Offset has no counterpart in relindex.Query and ADR-0003 fixes
// the Store at four methods, so limit+offset rows are asked for (capped at
// MaxScanRows) and the offset is sliced off in process. That is only
// meaningful because the store orders deterministically, which is its
// property and not something this package can fix. QueryResponse.Total is the
// row count the store returned BEFORE slicing, and is a LOWER BOUND: a
// four-method Store has no COUNT.
//
// # Multi-tenancy, and what this verb cannot do about it
//
// QueryRequest carries no namespace, relindex.Release has no namespace
// column, and the index is one SQLite file per process, so this verb is
// inherently CLUSTER-WIDE. Its results include Info.DownloadURL, which for a
// private tracker embeds a passkey. Those URLs are never logged -- but they
// ARE returned, because the download verb cannot resolve a guid without a URL
// and stripping them would break the path this verb exists to serve.
//
// Ruling R4 keeps this theoretical: the verb has zero callers today. Before
// it gets a real one (the M6 Torznab facade, or a query-mode Search),
// QueryRequest must take a namespace and the index must gain a namespace
// column. Do NOT add either now -- an API field with no consumer is how a
// field ships wrong.
package query

import (
	"context"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Handle must stay assignable to the search service's QueryFn. The signature
// is restated rather than imported: a named func type accepts a plain func of
// the same signature, and importing indexarr/search would couple two packages
// that have no other reason to know about each other.
var _ func(context.Context, schema.QueryRequest) schema.QueryResponse = (&Service{}).Handle
