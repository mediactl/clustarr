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

import (
	"regexp"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// wantSeries is the amendment's §A2.3 table: every collector Register must
// carry, by exact name. It is the source of truth for
// TestCatalogueMatchesTheAmendmentTable.
var wantSeries = []string{
	"clustarr_download_bytes_total",
	"clustarr_download_speed_bytes_per_second",
	"clustarr_downloads_active",
	"clustarr_download_duration_seconds",
	"clustarr_downloads_completed_total",
	"clustarr_import_files_total",
	"clustarr_import_unmatched_total",
	"clustarr_indexer_query_duration_seconds",
	"clustarr_indexer_queries_total",
	"clustarr_indexer_releases_returned",
	"clustarr_search_decisions_total",
	"clustarr_transcode_jobs_active",
	"clustarr_transcode_duration_seconds",
	"clustarr_transcode_speed_ratio",
	"clustarr_transcode_size_ratio",
	"clustarr_subtitle_fetches_total",
	"clustarr_provider_quota_remaining",
	"clustarr_work_queue_pending",
	"clustarr_work_handled_total",
	"clustarr_work_duration_seconds",
	"clustarr_reconcile_errors_total",
}

// fqNameRE and variableLabelsRE pull the fully-qualified name and the
// variable label names out of a *prometheus.Desc's String() form, e.g.:
//
//	Desc{fqName: "clustarr_download_bytes_total", help: "...", unit: "",
//	constLabels: {}, variableLabels: {protocol,client}}
//
// This works from Describe alone, before anything has been observed, which
// is the point: Gather only returns families with at least one recorded
// sample, and a *Vec metric has none until a label combination is used, so
// asserting straight off Describe is what lets these tests run without
// first touching every collector by hand.
var (
	fqNameRE         = regexp.MustCompile(`fqName: "([^"]*)"`)
	variableLabelsRE = regexp.MustCompile(`variableLabels: \{([^}]*)\}\}$`)
)

func descFQName(t *testing.T, d *prometheus.Desc) string {
	t.Helper()
	m := fqNameRE.FindStringSubmatch(d.String())
	require.NotNil(t, m, "could not parse fqName from %s", d.String())
	return m[1]
}

func descLabelNames(t *testing.T, d *prometheus.Desc) []string {
	t.Helper()
	m := variableLabelsRE.FindStringSubmatch(d.String())
	require.NotNil(t, m, "could not parse variableLabels from %s", d.String())
	if m[1] == "" {
		return nil
	}
	return strings.Split(m[1], ",")
}

// allDescs walks every collector in [all] and drains its Describe channel.
// A single Vec collector yields exactly one Desc, regardless of how many
// (or how few) label combinations have ever been observed.
func allDescs(t *testing.T) []*prometheus.Desc {
	t.Helper()
	var descs []*prometheus.Desc
	for _, c := range all {
		ch := make(chan *prometheus.Desc)
		go func() {
			c.Describe(ch)
			close(ch)
		}()
		for d := range ch {
			descs = append(descs, d)
		}
	}
	return descs
}

// TestCatalogueMatchesTheAmendmentTable is the shape test: exactly the 21
// series the amendment tables, by name, no more and no fewer.
func TestCatalogueMatchesTheAmendmentTable(t *testing.T) {
	descs := allDescs(t)
	require.Len(t, descs, len(wantSeries), "amendment §A2.3 tables %d series", len(wantSeries))

	got := make(map[string]bool, len(descs))
	for _, d := range descs {
		got[descFQName(t, d)] = true
	}
	for _, name := range wantSeries {
		require.True(t, got[name], "missing series %s", name)
	}
}

// TestEveryMetricUsesTheClustarrPrefix is the Prometheus naming convention
// the amendment requires (§A2.3): every family name starts with clustarr_.
func TestEveryMetricUsesTheClustarrPrefix(t *testing.T) {
	descs := allDescs(t)
	require.NotEmpty(t, descs)
	for _, d := range descs {
		name := descFQName(t, d)
		require.True(t, strings.HasPrefix(name, "clustarr_"), name)
	}
}

// TestNoMetricIsLabelledByAnUnboundedDimension is the rule that matters
// most: no metric may be labelled by anything unbounded. Media title, file
// path and release name are the ones the amendment calls out by name; url
// and query cover indexer/provider request details that carry the same
// risk.
func TestNoMetricIsLabelledByAnUnboundedDimension(t *testing.T) {
	banned := []string{"title", "path", "file", "release", "name", "url", "query"}
	for _, d := range allDescs(t) {
		for _, l := range descLabelNames(t, d) {
			for _, b := range banned {
				require.NotContains(t, strings.ToLower(l), b,
					"metric %s labelled by unbounded dimension %s", descFQName(t, d), l)
			}
		}
	}
}

// TestRegisterSucceedsAgainstAFreshRegistry catches the ordinary
// registration failures Describe-only checks cannot: duplicate fqNames,
// inconsistent help strings across a shared name, invalid label names.
func TestRegisterSucceedsAgainstAFreshRegistry(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, Register(reg))
}
