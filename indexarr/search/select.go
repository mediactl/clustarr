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

package search

import (
	"time"

	"k8s.io/utils/ptr"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	idxstatus "github.com/mediactl/clustarr/indexarr/status"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// candidate is one Indexer the request is in scope for. A non-empty Skip
// means it will not be queried and gets a named "skipped" outcome.
//
// An Indexer the request did not ask for is NOT a candidate at all and
// produces no outcome: the caller caps status.indexerOutcomes at 100 entries
// and keeps the first of each name (catalogarr/worker/search/worker.go), so
// two hundred "you did not ask for me" entries would push out the outcomes
// that matter.
type candidate struct {
	Indexer *indexv1alpha1.Indexer
	Skip    string
}

// The skip reasons are a closed set. They travel to the caller's
// status.indexerOutcomes[].error and, through the "skipped" metric outcome,
// into an operator's dashboard, so none of them may ever interpolate an
// indexer-supplied string. The one exception is the mode name, which is one
// of torznab.SearchMode's six constants and is ours, not the indexer's.
const (
	skipDisabled      = "disabled"
	skipNoAuto        = "automatic search disabled"
	skipNoInteractive = "interactive search disabled"
	skipProtocol      = "protocol not requested"
	skipNoCaps        = "caps not probed"
	skipNoCategory    = "no requested category is served by this indexer"
	skipNoIDParam     = "no supported id parameter for this request"
	skipUnhealthy     = "unhealthy or in backoff"
	skipQueryLimit    = "query limit reached"
)

// skipNoMode names the mode the indexer does not advertise.
func skipNoMode(mode torznab.SearchMode) string {
	return "does not support mode " + string(mode)
}

// selectCandidates decides, from the Indexer objects alone, which indexers
// this request is in scope for and which of those can answer it.
//
// It is pure: no clock of its own, no client, no network. The gate order is
// cheapest first, and health is checked before anything that would build a
// client.
func selectCandidates(
	idxs []indexv1alpha1.Indexer,
	req schema.SearchRequest,
	mode torznab.SearchMode,
	now time.Time,
) []candidate {
	scope := make(map[string]struct{}, len(req.IndexerRefs))
	for _, r := range req.IndexerRefs {
		scope[r.Namespace+"/"+r.Name] = struct{}{}
	}
	protocols := make(map[commonv1.Protocol]struct{}, len(req.Protocols))
	for _, p := range req.Protocols {
		protocols[p] = struct{}{}
	}

	out := make([]candidate, 0, len(idxs))
	for i := range idxs {
		idx := &idxs[i]
		if len(scope) > 0 {
			if _, ok := scope[idx.Namespace+"/"+idx.Name]; !ok {
				continue // not asked for: not a candidate, no outcome
			}
		}
		c := candidate{Indexer: idx}
		switch {
		case !ptr.Deref(idx.Spec.Enabled, true):
			c.Skip = skipDisabled
		case !req.UserInvoked && !ptr.Deref(idx.Spec.EnableAutomaticSearch, true):
			c.Skip = skipNoAuto
		case req.UserInvoked && !ptr.Deref(idx.Spec.EnableInteractiveSearch, true):
			c.Skip = skipNoInteractive
		case len(protocols) > 0 && !inProtocols(protocols, idx.Status.Protocol):
			c.Skip = skipProtocol
		case !idxstatus.Healthy(idx.Status, now):
			c.Skip = skipUnhealthy
		case atQueryLimit(idx):
			c.Skip = skipQueryLimit
		case idx.Status.Caps == nil:
			c.Skip = skipNoCaps
		case !idxstatus.SupportsMode(*idx.Status.Caps, string(mode)):
			c.Skip = skipNoMode(mode)
		case len(req.Categories) > 0 && len(queryCategories(req.Categories, idx.Status.Caps)) == 0:
			c.Skip = skipNoCategory
		}
		out = append(out, c)
	}
	return out
}

// inProtocols reports whether the indexer's resolved protocol is one the
// request asked for.
func inProtocols(want map[commonv1.Protocol]struct{}, got commonv1.Protocol) bool {
	_, ok := want[got]
	return ok
}

// atQueryLimit mirrors Prowlarr's IndexerLimitService.
//
// status.queriesInWindow is a PROJECTION of the query ring in
// clustarr-indexer-limits (see limits.go), not a running total, which is what
// makes this gate recoverable: a monotonic counter with no reset would skip
// an indexer with a configured queryLimit forever, because nothing else in
// the system ever lowers it.
func atQueryLimit(idx *indexv1alpha1.Indexer) bool {
	if idx.Spec.Limits == nil || idx.Spec.Limits.QueryLimit == nil {
		return false
	}
	return idx.Status.QueriesInWindow >= *idx.Spec.Limits.QueryLimit
}

// resolveQuery finalises a candidate: it builds the query and turns a "no
// usable id parameter" verdict into a skip.
//
// It is separate from selectCandidates because that verdict needs
// buildQuery's answer, and keeping both pure is what makes each testable
// without a cluster.
func resolveQuery(
	c candidate,
	req schema.SearchRequest,
	mode torznab.SearchMode,
	limit int,
) (torznab.Query, string) {
	q, ok := buildQuery(req, c.Indexer, mode, limit)
	if !ok {
		return torznab.Query{}, skipNoIDParam
	}
	return q, ""
}
