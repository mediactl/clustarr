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
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/relindex"
)

// localIndexLabel keeps this verb on the same dashboards as the indexer
// metrics without adding an unbounded label value. It is a CONSTANT: no
// caller-supplied string is ever a metric label.
const localIndexLabel = "_local-index"

// The closed set of metric outcome values for this verb.
const (
	outcomeOK            = "ok"
	outcomeInvalidFilter = "invalid_filter"
	outcomeStoreError    = "store_error"
	outcomeNotConfigured = "not_configured"
)

// Service is the rpc.indexarr.query handler. It has exactly one dependency
// and it is the local store; TestHandleMakesNoOutboundCall pins that.
type Service struct {
	// Store is the local release index. A nil Store answers with a
	// populated Error rather than panicking.
	Store relindex.Store

	// Now is the clock. nil means time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Handle is the rpc.indexarr.query body. It reads the LOCAL INDEX ONLY. It
// never returns an error: after a successful decode every failure is a
// populated QueryResponse.Error, and an empty result set is a success with no
// releases.
func (s *Service) Handle(ctx context.Context, req schema.QueryRequest) schema.QueryResponse {
	ctx, span := tracing.Start(ctx, "indexarr.query")
	defer span.End()
	start := s.now()
	defer func() {
		metrics.IndexerQueryDuration.WithLabelValues(localIndexLabel, "query").
			Observe(s.now().Sub(start).Seconds())
	}()

	resp, outcome := s.handle(ctx, req)
	metrics.IndexerQueriesTotal.WithLabelValues(localIndexLabel, outcome).Inc()
	span.SetAttributes(
		attribute.Int("query.limit", int(req.Limit)),
		attribute.Int("query.offset", int(req.Offset)),
		// The TEXT is never an attribute: attacker-controlled and unbounded.
		attribute.Int("query.text_len", len(req.Text)),
		attribute.Int("query.rows", len(resp.Releases)),
	)
	if resp.Error != "" {
		tracing.RecordError(span, errors.New(resp.Error))
	}
	return resp
}

func (s *Service) handle(
	ctx context.Context, req schema.QueryRequest,
) (schema.QueryResponse, string) {
	if s.Store == nil {
		return schema.QueryResponse{
			Error: "indexarr: release index is not configured",
		}, outcomeNotConfigured
	}
	q, err := buildQuery(req)
	if err != nil {
		return schema.QueryResponse{
			Error: truncate(err.Error(), maxErrorChars),
		}, outcomeInvalidFilter
	}

	ctx, span := tracing.Start(ctx, "indexarr.query.store")
	rows, err := s.Store.Search(ctx, q)
	if err != nil {
		tracing.RecordError(span, err)
		span.End()
		return schema.QueryResponse{
			Error: truncate("indexarr: query the release index: "+err.Error(), maxErrorChars),
		}, outcomeStoreError
	}
	span.End()

	limit := max(q.Limit-int(req.Offset), 0)
	return schema.QueryResponse{
		Releases: decodeRows(ctx, rows, int(req.Offset), limit),
		// Total is the matched count BEFORE paging -- and a LOWER BOUND: a
		// four-method Store has no COUNT, so when the over-fetch hit
		// MaxScanRows this is exactly that ceiling and not the true total.
		Total: int64(len(rows)),
	}, outcomeOK
}

// decodeRows replays each row's InfoJSON -- the full schema.Release, stored
// exactly so a query needs no re-query. A row that will not decode is skipped
// and logged: one corrupt row must not fail a whole page.
//
// The reply is bounded by MaxReplyBytes because a QueryResponse is one NATS
// message and the broker's max_payload is 8Mi.
func decodeRows(
	ctx context.Context, rows []relindex.Release, offset, limit int,
) []schema.Release {
	if offset >= len(rows) || limit == 0 {
		return nil
	}
	log := logging.FromContext(ctx)
	out := make([]schema.Release, 0, min(limit, len(rows)-offset))
	budget := MaxReplyBytes
	for _, r := range rows[offset:] {
		var rel schema.Release
		if err := json.Unmarshal(r.InfoJSON, &rel); err != nil {
			log.Warn("indexarr/query: skipping a row whose infoJSON will not decode",
				"indexer", r.Indexer, "err", err)
			continue
		}
		if budget -= len(r.InfoJSON); budget < 0 {
			break
		}
		out = append(out, rel)
		if len(out) == limit {
			break
		}
	}
	return out
}
