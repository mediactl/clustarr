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

package scenemap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/httpjson"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultBaseURL is TheXEM's root; every call is under /map.
const DefaultBaseURL = "https://thexem.info"

// DefaultRate and DefaultBurst are chosen, not published: TheXEM documents
// no limit, and Cached makes a handful of calls a day per series.
const (
	DefaultRate  rate.Limit = 1
	DefaultBurst int        = 2
)

// ErrFailure is TheXEM's own {"result":"failure"} envelope, for any
// failure other than the two Sonarr ignores (see ignoredFailures).
var ErrFailure = errors.New("scenemap: thexem reported failure")

// ignoredFailures are the failure messages that mean "no table", not "no
// answer" -- Sonarr's XemProxy.IgnoredErrors, verbatim. "no show with the
// tvdb_id" is what /map/all answers for a series TheXEM does not map
// (verified live: {"result":"failure","data":[],"message":"no show with
// the tvdb_id 81797 found"}).
var ignoredFailures = []string{"no single connection", "no show with the tvdb_id"}

// XEMConfig configures an XEM client.
type XEMConfig struct {
	HTTPClient *http.Client
	BaseURL    string
	Limiter    *rate.Limiter
	UserAgent  string
}

// XEM is a TheXEM client. Every call adds origin=tvdb, as Sonarr's
// request builder does: TVDB numbering is the catalog's.
type XEM struct {
	h       *httpjson.Client
	baseURL string
}

// NewXEM builds an XEM client.
func NewXEM(cfg XEMConfig) *XEM {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return &XEM{h: &httpjson.Client{Provider: "thexem", HTTP: cfg.HTTPClient, Limiter: cfg.Limiter, UserAgent: cfg.UserAgent}, baseURL: base}
}

type envelope struct {
	Result  string          `json:"result"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
}

// get fetches one /map endpoint and returns its data, or nil for an
// ignored failure.
func (x *XEM) get(ctx context.Context, resource string, q url.Values) (json.RawMessage, error) {
	q.Set("origin", "tvdb")
	var env envelope
	if err := x.h.GetJSON(ctx, x.baseURL+"/map/"+resource+"?"+q.Encode(), nil, &env); err != nil {
		return nil, err
	}
	if strings.EqualFold(env.Result, "failure") {
		for _, ignored := range ignoredFailures {
			if strings.Contains(env.Message, ignored) {
				return nil, nil
			}
		}
		return nil, fmt.Errorf("%w: %s: %s", ErrFailure, resource, env.Message)
	}
	return env.Data, nil
}

// HaveMap returns the TVDB series TheXEM holds a table for. The ids arrive
// as strings; one that does not parse to a positive integer is skipped, as
// Sonarr's GetXemSeriesIds does.
func (x *XEM) HaveMap(ctx context.Context) ([]int64, error) {
	ctx, span := tracing.Start(ctx, "scenemap.thexem.HaveMap")
	defer span.End()

	data, err := x.get(ctx, "havemap", url.Values{})
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var raw []json.RawMessage
	if err := x.h.Decode(data, &raw); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	ids := make([]int64, 0, len(raw))
	for _, r := range raw {
		s := strings.Trim(string(r), `"`)
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			ids = append(ids, n)
		}
	}
	return ids, nil
}

type xemValues struct {
	Season   int `json:"season"`
	Episode  int `json:"episode"`
	Absolute int `json:"absolute"`
}

// Mappings returns one series' rows. Following Sonarr, a row with no scene
// numbering is dropped (XemProxy.GetSceneTvdbMappings keeps only rows whose
// Scene is set), as is a row whose scene numbering is all zeros
// (XemService.PerformUpdate: "Mapping ... is invalid, skipping"); so is a
// row with no TVDB numbering, which has nothing to map onto. A series
// TheXEM does not map returns no rows and no error.
func (x *XEM) Mappings(ctx context.Context, tvdbID int64) ([]Mapping, error) {
	ctx, span := tracing.Start(ctx, "scenemap.thexem.Mappings")
	defer span.End()

	if tvdbID <= 0 {
		err := fmt.Errorf("scenemap: tvdb id %d: %w", tvdbID, metadata.ErrUnsupported)
		tracing.RecordError(span, err)
		return nil, err
	}
	data, err := x.get(ctx, "all", url.Values{"id": {strconv.FormatInt(tvdbID, 10)}})
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var rows []struct {
		Scene *xemValues `json:"scene"`
		TVDB  *xemValues `json:"tvdb"`
	}
	if err := x.h.Decode(data, &rows); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	out := make([]Mapping, 0, len(rows))
	for _, r := range rows {
		if r.Scene == nil || r.TVDB == nil || *r.Scene == (xemValues{}) {
			continue
		}
		out = append(out, Mapping{Scene: Numbering(*r.Scene), TVDB: Numbering(*r.TVDB)})
	}
	return out, nil
}

// Names returns every mapped series' scene titles, keyed by TVDB id. A
// season of -1 is the whole series (nil); a season that is not an integer
// is skipped, as Sonarr's GetSceneTvdbNames skips it.
//
// Sonarr also drops every season above 1 for TVDB 79151, a workaround for
// bad Fate/Zero data at the time; TheXEM's names for 79151 today carry
// only -1, so the hard-coded exception is not ported.
func (x *XEM) Names(ctx context.Context) (map[int64][]SceneName, error) {
	ctx, span := tracing.Start(ctx, "scenemap.thexem.Names")
	defer span.End()

	data, err := x.get(ctx, "allNames", url.Values{"seasonNumbers": {"1"}})
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	out := map[int64][]SceneName{}
	if data == nil {
		return out, nil
	}
	var raw map[string][]map[string]json.RawMessage
	if err := x.h.Decode(data, &raw); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	for key, names := range raw {
		id, err := strconv.ParseInt(key, 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		for _, n := range names {
			for title, v := range n {
				season, err := strconv.Atoi(string(v))
				if err != nil || title == "" {
					continue
				}
				name := SceneName{Title: title}
				if season >= 0 {
					name.Season = &season
				}
				out[id] = append(out[id], name)
			}
		}
	}
	return out, nil
}

// Ping proves TheXEM answers, with the havemap call Cached makes anyway.
func (x *XEM) Ping(ctx context.Context) error {
	_, err := x.HaveMap(ctx)
	return err
}
