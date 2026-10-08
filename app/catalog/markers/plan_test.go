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

package markers_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/markers"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// smokeFileMs is the smoke test's fixture runtime (status.mediaInfo.
// runtimeMillis): a 10 s clip of a real title, for which TheIntroDB answers
// the real title's timings.
const smokeFileMs = 10_031

// theIntroDBAnswer is the metadata gateway's clustarr-markers record for
// hash-1, answered Found with segs, as its TheIntroDB worker writes it.
func theIntroDBAnswer(now time.Time, segs ...schema.SegmentJSON) *schema.MarkersRecord {
	at := now.Add(-time.Minute)
	rec := &schema.MarkersRecord{
		Inputs: schema.MarkersInputs{ProbeHash: "hash-1", DurationMs: smokeFileMs},
		Answer: &schema.MarkersAnswer{Result: string(catalogv1alpha1.MarkersFound), Segments: segs, FetchedAt: at},
	}
	rec.Schema, rec.Seq, rec.State, rec.AnsweredAt = schema.MarkersRecordSchema, 1, records.StateAnswered, &at
	return rec
}

func probedInput(kind commonv1.MediaKind, now time.Time) markers.Input {
	return markers.Input{
		Kind: kind, File: schema.Ref{Namespace: "media", Name: "file", UID: "uid-file"},
		ProbeHash: "hash-1", Probed: true, DurationMs: smokeFileMs, Now: now,
		Query: markers.Query{Ask: &schema.MarkersQuery{IDs: map[string]string{"tmdb": "27205"}}},
	}
}

// TestTheIntroDBSegmentsPastTheEndAreNotStored is smoke finding 6:
// TheIntroDB's intro at 0-38 s on Inception's 10 s file and at 437-538 s on
// GoT S01E01's reached status.markers and, from there, Plex. Incorporating
// the answer clamps an end past the probe's runtime and drops a segment
// starting past it; a stored block that holds one is merged again, though
// nothing else changed.
func TestTheIntroDBSegmentsPastTheEndAreNotStored(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	intro := func(start, end int64) schema.SegmentJSON {
		return schema.SegmentJSON{Kind: "intro", StartMs: start, EndMs: end, Source: "theintrodb", Confidence: 100}
	}
	stored := func(start, end int64) catalogv1alpha1.MarkerSegment {
		return catalogv1alpha1.MarkerSegment{Kind: catalogv1alpha1.MarkerIntro, StartMs: start, EndMs: end,
			Source: catalogv1alpha1.SegmentSourceTheIntroDB, Confidence: 100}
	}

	t.Run("an answer's end past the runtime is clamped", func(t *testing.T) {
		in := probedInput(commonv1.MediaKindMovie, now)
		in.Record, in.RecordOK, in.RecordRead = theIntroDBAnswer(now, intro(0, 38_000)), true, true
		d := markers.Plan(in)
		require.NotNil(t, d.Markers)
		require.Equal(t, catalogv1alpha1.MarkersFound, d.Markers.Result)
		require.Equal(t, []catalogv1alpha1.MarkerSegment{stored(0, smokeFileMs)}, d.Markers.Segments)
	})
	t.Run("an answer's segment past the end is dropped", func(t *testing.T) {
		in := probedInput(commonv1.MediaKindEpisode, now)
		in.Record, in.RecordOK, in.RecordRead = theIntroDBAnswer(now, intro(437_388, 537_777)), true, true
		d := markers.Plan(in)
		require.NotNil(t, d.Markers)
		require.Equal(t, catalogv1alpha1.MarkersFound, d.Markers.Result)
		require.Empty(t, d.Markers.Segments)
	})
	t.Run("a stored block past the end is merged again", func(t *testing.T) {
		in := probedInput(commonv1.MediaKindMovie, now)
		in.Prev = &catalogv1alpha1.FileMarkers{
			Result: catalogv1alpha1.MarkersFound, ForProbeHash: "hash-1", DurationMs: smokeFileMs,
			FetchedAt: metav1.NewTime(now.Add(-time.Hour)),
			Segments:  []catalogv1alpha1.MarkerSegment{stored(0, 38_000), stored(437_388, 537_777)},
		}
		d := markers.Plan(in)
		require.Nil(t, d.Request, "not due: TheIntroDB is not asked again")
		require.NotNil(t, d.Markers)
		require.Equal(t, []catalogv1alpha1.MarkerSegment{stored(0, smokeFileMs)}, d.Markers.Segments)
		require.Equal(t, in.Prev.FetchedAt, d.Markers.FetchedAt, "TheIntroDB's outcome is kept")
	})
	t.Run("a stored block within the runtime is carried verbatim", func(t *testing.T) {
		in := probedInput(commonv1.MediaKindMovie, now)
		in.Prev = &catalogv1alpha1.FileMarkers{
			Result: catalogv1alpha1.MarkersFound, ForProbeHash: "hash-1", DurationMs: smokeFileMs,
			FetchedAt: metav1.NewTime(now.Add(-time.Hour)),
			Segments:  []catalogv1alpha1.MarkerSegment{{Kind: catalogv1alpha1.MarkerIntro, StartMs: 0, EndMs: 5_000}},
		}
		require.Same(t, in.Prev, markers.Plan(in).Markers, "an untagged legacy block is never rewritten for nothing")
	})
}
