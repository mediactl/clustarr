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

package segmenting_test

import (
	"context"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/segments"
)

func schemaResult(hash string) schema.SegmentsResult {
	return schema.SegmentsResult{
		MediaFile: "andor-s01e02", ProbeHash: hash, Version: segments.AnalyzerVersion, Result: "Found",
		Segments: []schema.SegmentJSON{{Kind: "credits", StartMs: 2_600_000, EndMs: 2_700_000, Source: "analysis", Confidence: 90}},
	}
}

func schemaEncode(t *testing.T, p schema.Payload) (string, []byte, error) {
	t.Helper()
	return schema.Encode(p)
}

type resultMsg struct{ env *events.Envelope }

func (m resultMsg) Envelope() *events.Envelope               { return m.env }
func (m resultMsg) Subject() string                          { return "" }
func (m resultMsg) Attempt() uint64                          { return 1 }
func (m resultMsg) Ack(context.Context) error                { return nil }
func (m resultMsg) Nak(context.Context, time.Duration) error { return nil }
func (m resultMsg) Term(context.Context, string) error       { return nil }
func (m resultMsg) InProgress(context.Context) error         { return nil }
