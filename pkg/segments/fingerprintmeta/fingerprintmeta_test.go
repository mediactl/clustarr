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

package fingerprintmeta_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/segments"
	"github.com/mediactl/clustarr/pkg/segments/chromaprint"
	"github.com/mediactl/clustarr/pkg/segments/fingerprintmeta"
)

// TestSampleRateIsChromaprints pins the restated rate to the analyser's:
// the package restates it so the manager, which backfills the metadata,
// links no Chromaprint (test/guards' deps guards), and only this test keeps
// the two from drifting apart -- a drift would make every cached
// fingerprint read as mismatched and be recomputed.
func TestSampleRateIsChromaprints(t *testing.T) {
	assert.Equal(t, chromaprint.SampleRate, fingerprintmeta.SampleRate)
}

// TestBuildMatchesWhatItDescribes: a fingerprint's metadata from Build is
// accepted by Mismatch for the window it describes, and refused for another
// window length or an older fingerprint version.
func TestBuildMatchesWhatItDescribes(t *testing.T) {
	meta := fingerprintmeta.Build("abc123", "start", 90, "markers@v0.0.0-test")
	info := events.ObjectInfo{Name: segments.FingerprintKey("abc123", "start"), Metadata: meta.Metadata}
	assert.Empty(t, fingerprintmeta.Mismatch(info, 90))
	assert.Contains(t, fingerprintmeta.Mismatch(info, 120), "window-seconds")

	old, ok := fingerprintmeta.Backfill("abc123.start", "manager@v0.0.0-test")
	assert.True(t, ok)
	assert.Contains(t, fingerprintmeta.Mismatch(events.ObjectInfo{Metadata: old.Metadata}, 90), "fingerprint-version")

	probeHash, which, v, ok := fingerprintmeta.ParseKey(segments.FingerprintKey("abc123", "end"))
	assert.True(t, ok)
	assert.Equal(t, []any{"abc123", "end", segments.FingerprintVersion}, []any{probeHash, which, v})
}
