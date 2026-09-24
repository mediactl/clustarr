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

package query

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/relindex"
)

const (
	// DefaultLimit applies when QueryRequest.Limit is zero. relindex's Store
	// "never invents one", so this verb must always send a positive limit.
	DefaultLimit = 100

	// MaxScanRows ceilings limit+offset. QueryRequest.Offset has no
	// counterpart in relindex.Query and ADR-0003 fixes the Store at four
	// methods, so paging is over-fetch plus slice, and the over-fetch is
	// bounded.
	MaxScanRows = 2000

	// MaxReplyBytes is the same broker-payload budget the download verb
	// uses: a QueryResponse is one NATS message and max_payload is 8Mi
	// (config/nats/configmap.yaml:20).
	MaxReplyBytes = 4 << 20

	// maxFilterKeyChars bounds a caller-supplied key before it is quoted
	// into an error message that reaches another controller's status.
	maxFilterKeyChars = 64

	// maxIndexerFilters bounds the indexer list. Every name becomes a bound
	// parameter in an IN clause.
	maxIndexerFilters = 32

	// maxErrorChars bounds QueryResponse.Error, for the same reason.
	maxErrorChars = 512
)

// errUnmatchable says the request is well-formed but cannot match anything,
// as distinct from malformed. The handler turns it into an EMPTY RESULT SET,
// not an Error: the caller asked a question with no possible answer, which is
// a successful "nothing", the same as a text query that matches no row.
//
// It exists because every restriction in this file degrades to "no
// restriction" when it parses to nothing, and relindex reads each of those as
// the whole corpus:
//
//   - relindex.Search omits the MATCH clause entirely for an empty
//     Query.Text, so an unmatchable TEXT would return every release in the
//     index;
//   - an empty Query.Indexers or Query.Categories emits no IN clause, so an
//     explicitly-empty indexer or category filter would do the same.
//
// Returning more data than the caller asked for, with no way for them to
// tell, is the failure this package rejects an unknown filter to avoid --
// and it is worse here, because this verb is cluster-wide and its results
// carry passkey-bearing download URLs.
var errUnmatchable = errors.New("query: request cannot match any release")

// filterKeys is the CLOSED set this verb understands. An unknown key is an
// ERROR, not a silent no-op: dropping a filter silently returns MORE data than
// the caller asked for, and the caller cannot tell.
//
// It is sorted, because it is also the "known filters are ..." list in the
// error message.
var filterKeys = []string{"category", "indexer", "protocol", "since"}

func knownFilter(k string) bool { return slices.Contains(filterKeys, k) }

// buildQuery maps a QueryRequest onto relindex.Query. It is pure: no I/O, no
// clock, no store.
func buildQuery(req schema.QueryRequest) (relindex.Query, error) {
	if req.Limit < 0 || req.Offset < 0 {
		return relindex.Query{}, errors.New("query: limit and offset must not be negative")
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit > schema.MaxSearchReleases {
		limit = schema.MaxSearchReleases
	}
	scan := min(limit+int(req.Offset), MaxScanRows)

	q := relindex.Query{
		// NORMALISED, not escaped. The two are different jobs and only one
		// of them belongs here.
		//
		// Escaping is the store's: pkg/relindex's matchExpr quotes every
		// token into an FTS5 string, and pre-escaping here would compose
		// two escapers into a query that matches nothing -- which is how an
		// injection "fix" becomes an outage.
		//
		// Normalising is the CALLER's, and relindex says so in as many
		// words: "the caller supplies Release.TitleNorm and must normalise
		// Query.Text with the same function, or nothing will match"
		// (pkg/relindex/doc.go, fts.go). The RSS worker and the search
		// fan-out fill that column with release.TitleNorm
		// (indexarr/worker/rss/worker.go, indexarr/search/fanout.go), so
		// this runs the same function. Passing raw text instead would make
		// "The Matrix" -- indexed as "matrix" -- return nothing, silently.
		Text:  release.TitleNorm(req.Text),
		Limit: scan,
	}

	// Text that normalises to nothing is UNMATCHABLE, not unfiltered.
	//
	// TitleNorm keeps letters, digits and marks in every script, so what
	// maps to "" is text with none at all -- "!!!", "^", "\x00", "—".
	// relindex.Search reads an empty Query.Text as "no text filter" and
	// returns the whole corpus, so without this a punctuation-only query
	// would answer with every release indexarr has ever seen.
	//
	// An EMPTY req.Text is the opposite and must keep meaning "no text
	// filter": that is a filters-only browse, and it asked for no text
	// restriction in the first place. The two are deliberately not
	// collapsed.
	//
	// A non-Latin query is NOT in this class any more. Both sides used to
	// go through release.CleanTitle, which keeps only [a-z0-9 ]: "матрица"
	// normalised to "" here, and "日本語のタイトル 2026" degraded to "2026"
	// and matched every release of that year, while the indexed column had
	// lost the same title tokens. TitleNorm on both sides (the index write
	// and this query, switched in one change) keeps "матрица" as a term,
	// so a Cyrillic or CJK title is findable by its own title.
	if req.Text != "" && q.Text == "" {
		return relindex.Query{}, errUnmatchable
	}

	// Sorted, so two bad filters give a deterministic message rather than
	// one that changes with Go's map iteration order.
	for _, k := range slices.Sorted(maps.Keys(req.Filters)) {
		v := strings.TrimSpace(req.Filters[k])
		if !knownFilter(k) {
			return relindex.Query{}, fmt.Errorf("query: unknown filter %q; known filters are %s",
				truncate(k, maxFilterKeyChars), strings.Join(filterKeys, ", "))
		}
		switch k {
		case "protocol":
			if v != string(commonv1.ProtocolTorrent) && v != string(commonv1.ProtocolUsenet) {
				return relindex.Query{}, errors.New(
					"query: filter protocol must be torrent or usenet")
			}
			q.Protocol = v
		case "indexer":
			for name := range strings.SplitSeq(v, ",") {
				if name = strings.TrimSpace(name); name != "" {
					q.Indexers = append(q.Indexers, name)
				}
			}
			if len(q.Indexers) == 0 {
				// `{"indexer": ""}` or `{"indexer": " , "}`. relindex emits
				// no IN clause for an empty list, so leaving it nil would
				// turn "restrict to these indexers" into "every indexer".
				return relindex.Query{}, errUnmatchable
			}
			if len(q.Indexers) > maxIndexerFilters {
				return relindex.Query{}, fmt.Errorf(
					"query: filter indexer lists more than %d indexers", maxIndexerFilters)
			}
		case "category":
			for c := range strings.SplitSeq(v, ",") {
				c = strings.TrimSpace(c)
				if c == "" {
					continue
				}
				n, err := strconv.Atoi(c)
				if err != nil {
					return relindex.Query{}, errors.New(
						"query: filter category must be comma-separated newznab ids")
				}
				q.Categories = append(q.Categories, n)
			}
			if len(q.Categories) == 0 {
				// `{"category": ""}` or `{"category": " , "}`, for the same
				// reason as the indexer list above.
				return relindex.Query{}, errUnmatchable
			}
		case "since":
			ts, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return relindex.Query{}, errors.New("query: filter since must be RFC3339")
			}
			q.Since = &ts
		}
	}
	return q, nil
}

// truncate bounds an attacker-controlled string. The ellipsis is inside the
// budget, so the result is never longer than maxLen.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen < 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
