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

package main

import (
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

// kedaSelector matches one prometheus-nats-exporter selector in a KEDA
// query: the metric, its stream_name and its consumer_name matcher.
var kedaSelector = regexp.MustCompile(
	`(jetstream_consumer_\w+)\{stream_name="([^"]+)", consumer_name(=~|=)"([^"]+)"\}`)

// TestCaptionarrScaledObjectCountsTheRealFetchConsumers holds the opt-in
// KEDA trigger for captionarr-worker to the consumers the worker actually
// drains. Until plan task F-6 both copies named consumer "captionarr-fetch",
// which pkg/events never declared -- the fetch consumers are
// captionarr-fetch-high and captionarr-fetch-normal -- so the query summed
// no series, read zero however deep the queue, and KEDA held the worker at
// its floor. Nothing fails when a Prometheus selector matches nothing, which
// is why this is a test and not a review note.
func TestCaptionarrScaledObjectCountsTheRealFetchConsumers(t *testing.T) {
	var want []string
	for _, c := range events.Default().Consumers {
		if c.Stream == events.StreamWorkCaptionarr {
			want = append(want, c.Name)
		}
	}
	slices.Sort(want)
	require.NotEmpty(t, want, "setup: the topology declares no captionarr consumer")

	for _, path := range []string{
		"../../config/keda/captionarr-worker-scaledobject.yaml",
		"../../charts/clustarr/templates/scaledobject.yaml",
	} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			sels := kedaSelector.FindAllStringSubmatch(string(raw), -1)
			require.NotEmpty(t, sels, "no jetstream_consumer_* selector found; this guard is not looking where it thinks it is")
			for _, m := range sels {
				metric, stream, op, value := m[1], m[2], m[3], m[4]
				assert.Equal(t, events.StreamWorkCaptionarr, stream, "%s: stream_name", metric)
				// Prometheus anchors =~ matchers at both ends.
				pattern := regexp.QuoteMeta(value)
				if op == "=~" {
					pattern = value
				}
				re, err := regexp.Compile("^(?:" + pattern + ")$")
				require.NoError(t, err, "%s: consumer_name matcher", metric)
				var got []string
				for _, name := range want {
					if re.MatchString(name) {
						got = append(got, name)
					}
				}
				assert.Equal(t, want, got,
					"%s{consumer_name%s%q} must select every captionarr fetch consumer and nothing else", metric, op, value)
			}
		})
	}
}
