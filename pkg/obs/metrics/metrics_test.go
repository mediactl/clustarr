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
	"bufio"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// wantSeries is the amendment's §A2.3 table: the 23 series the design named,
// which every Register must still carry by exact name. The catalogue has
// grown past it since (77 series on 2026-11-10), so the whole catalogue is
// held to docs/observability.md instead, by TestCatalogueMatchesTheObservabilityDoc.
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
	"clustarr_indexer_releases_dropped_total",
	"clustarr_search_decisions_total",
	"clustarr_metadata_cache_hits_total",
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

// TestCatalogueMatchesTheAmendmentTable: every series the amendment's §A2.3
// table named is still registered, by exact name. It no longer counts the
// catalogue -- the amendment's 23 were the design's seed, and every series
// added since is tabled in docs/observability.md, which the next test holds
// the whole catalogue to.
func TestCatalogueMatchesTheAmendmentTable(t *testing.T) {
	descs := allDescs(t)
	got := make(map[string]bool, len(descs))
	for _, d := range descs {
		got[descFQName(t, d)] = true
	}
	for _, name := range wantSeries {
		require.True(t, got[name], "missing series %s", name)
	}
}

// catalogueDoc is the catalogue's authority: every series Register carries
// has a row in its "Metric catalogue" tables, with the labels the collector
// declares, and every row's series is registered -- except the rows
// registeredElsewhere names, whose "Recorded by" column says another
// registry carries them. "Adding a metric" in the doc says so: an
// undocumented metric is not discoverable by an operator who only reads
// that file.
const catalogueDoc = "../../../docs/observability.md"

// registeredElsewhere are the catalogue's rows Register does not carry,
// each with the registry that does. The test checks each still has a row
// and is still not registered here, so a series moved into this package
// drops its entry.
var registeredElsewhere = map[string]string{
	"clustarr_ffgo_abandoned_calls":     "pkg/ffruntime/ffmetrics, on the registry of every binary that links ffgo",
	"clustarr_segments_analyzed_total":  "app/segments/worker, markers' own registry",
	"clustarr_segments_stage_seconds":   "app/segments/worker, markers' own registry",
	"clustarr_segments_dnn_available":   "app/segments/worker, markers' own registry",
	"clustarr_livetv_grid_calls_total":  "app/livetv/tuner, on controller-runtime's registry in the manager",
	"clustarr_livetv_grid_hold_seconds": "app/livetv/tuner, on controller-runtime's registry in the manager",
}

var (
	// catalogueRowRE matches a table row of the catalogue: its series name
	// in the first cell, its type in the second, its labels in the third.
	catalogueRowRE = regexp.MustCompile("^\\| `(clustarr_[a-z0-9_]+)` \\| ([a-z]+) \\| ([^|]*)\\|")
	// parenthesisedRE strips a label's listed values, "`outcome` (`ok`,
	// `error`)", so only the label names are read.
	parenthesisedRE = regexp.MustCompile(`\([^)]*\)`)
	backtickedRE    = regexp.MustCompile("`([^`]+)`")
)

// documentedSeries reads the catalogue tables of docs/observability.md:
// each series' label names, in the order the row lists them.
func documentedSeries(t *testing.T) map[string][]string {
	t.Helper()
	f, err := os.Open(catalogueDoc)
	require.NoError(t, err)
	defer f.Close()
	out := map[string][]string{}
	in := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "## Metric catalogue"):
			in = true
			continue
		case strings.HasPrefix(line, "### Adding a metric"), strings.HasPrefix(line, "## Traces"):
			in = false
		}
		if !in {
			continue
		}
		m := catalogueRowRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		_, dup := out[name]
		require.False(t, dup, "%s is tabled twice in %s", name, catalogueDoc)
		labels := []string{}
		for _, l := range backtickedRE.FindAllStringSubmatch(parenthesisedRE.ReplaceAllString(m[3], ""), -1) {
			labels = append(labels, l[1])
		}
		out[name] = labels
	}
	require.NoError(t, sc.Err())
	require.NotEmpty(t, out, "no catalogue rows read from %s", catalogueDoc)
	return out
}

// TestCatalogueMatchesTheObservabilityDoc is the shape test: the series
// Register carries are exactly the catalogue's rows less registeredElsewhere,
// each with the labels its row lists, in order. Adding a series means adding
// its row in the same change -- which is the point.
func TestCatalogueMatchesTheObservabilityDoc(t *testing.T) {
	documented := documentedSeries(t)
	registered := map[string][]string{}
	for _, d := range allDescs(t) {
		name := descFQName(t, d)
		_, dup := registered[name]
		require.False(t, dup, "%s is registered twice", name)
		registered[name] = descLabelNames(t, d)
	}

	for name, where := range registeredElsewhere {
		_, tabled := documented[name]
		require.True(t, tabled, "%s is registeredElsewhere (%s) but has no row in %s", name, where, catalogueDoc)
		_, here := registered[name]
		require.False(t, here, "%s is registered here, not only %s; drop its registeredElsewhere entry", name, where)
		delete(documented, name)
	}

	names := func(m map[string][]string) []string {
		out := make([]string, 0, len(m))
		for n := range m {
			out = append(out, n)
		}
		sort.Strings(out)
		return out
	}
	require.Equal(t, names(documented), names(registered),
		"the catalogue's rows in %s and the series Register carries differ", catalogueDoc)
	for name, labels := range registered {
		want := documented[name]
		if want == nil {
			want = []string{}
		}
		if labels == nil {
			labels = []string{}
		}
		require.Equal(t, want, labels, "%s's row in %s lists other labels than the collector declares", name, catalogueDoc)
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

// bannedLabelWords name the unbounded dimensions: media title, file path
// and release name are the ones the amendment calls out by name; url and
// query cover indexer/provider request details that carry the same risk.
var bannedLabelWords = map[string]bool{"title": true, "path": true, "file": true, "release": true, "name": true, "url": true, "query": true}

// unboundedLabel is the one rule TestNoMetricIsLabelledByAnUnboundedDimension
// applies to the catalogue and TestTheUnboundedDimensionRuleMatchesWholeWords
// pins the shape of: a label is banned when one of its words -- the label
// name split on underscores -- is in bannedLabelWords, so `file_path` and
// `release_title` are caught while `profile` (a TranscodeProfile's name,
// 2026-09-30) is not: the substring rule read "profile" as "file".
func unboundedLabel(label string) bool {
	for _, word := range strings.Split(strings.ToLower(label), "_") {
		if bannedLabelWords[word] {
			return true
		}
	}
	return false
}

// TestNoMetricIsLabelledByAnUnboundedDimension is the rule that matters
// most: no metric may be labelled by anything unbounded, as unboundedLabel
// reads it.
func TestNoMetricIsLabelledByAnUnboundedDimension(t *testing.T) {
	for _, d := range allDescs(t) {
		for _, l := range descLabelNames(t, d) {
			require.False(t, unboundedLabel(l),
				"metric %s labelled by unbounded dimension %s", descFQName(t, d), l)
		}
	}
}

// TestTheUnboundedDimensionRuleMatchesWholeWords pins unboundedLabel's shape
// on label names the catalogue does not carry: a banned word anywhere in
// the name fails, a banned word inside another word does not. It tests the
// function the catalogue guard applies, not a copy of it, so a guard that
// slid back to the substring rule fails here too.
func TestTheUnboundedDimensionRuleMatchesWholeWords(t *testing.T) {
	for _, l := range []string{"file", "file_path", "release_title", "item_name", "Query"} {
		require.True(t, unboundedLabel(l), "%s names an unbounded dimension", l)
	}
	for _, l := range []string{"profile", "released", "unnamed", "pathology", "filesystem"} {
		require.False(t, unboundedLabel(l), "%s only contains a banned word", l)
	}
}

// TestRegisterSucceedsAgainstAFreshRegistry catches the ordinary
// registration failures Describe-only checks cannot: duplicate fqNames,
// inconsistent help strings across a shared name, invalid label names.
func TestRegisterSucceedsAgainstAFreshRegistry(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, Register(reg))
}

// TestDurationBucketsLongCoverDaysNotHours guards the ceiling
// durationBucketsLong needs to be useful for DownloadDuration and
// TranscodeDuration: downloads can sit starved for days and large
// transcodes can run most of a day, so a top boundary below
// durationBucketsLongMax would make histogram_quantile silently clamp
// exactly the slow/stuck tail those metrics exist to catch, instead of
// reporting it.
func TestDurationBucketsLongCoverDaysNotHours(t *testing.T) {
	require.NotEmpty(t, durationBucketsLong)
	max := durationBucketsLong[len(durationBucketsLong)-1]
	require.GreaterOrEqual(t, max, float64(durationBucketsLongMax),
		"durationBucketsLong tops out at %.0fs, need at least %ds (3 days) so a stuck download or transcode isn't clamped into the last bucket",
		max, durationBucketsLongMax)
}
