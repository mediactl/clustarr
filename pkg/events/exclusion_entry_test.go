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

package events_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

func TestExclusionEntryEncodeDecodeRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   events.ExclusionEntry
	}{
		{
			name: "full entry",
			in: events.ExclusionEntry{
				Namespace: "media", Name: "no-heat-1995",
				Kind: "movie", Reason: "already have a better cut",
			},
		},
		{
			name: "no reason given",
			in:   events.ExclusionEntry{Namespace: "media", Name: "no-heat-1995", Kind: "movie"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.in.Encode()
			require.NoError(t, err)

			out, err := events.DecodeExclusionEntry(data)
			require.NoError(t, err)
			assert.Equal(t, tc.in, out)
		})
	}
}

func TestDecodeExclusionEntryRejectsGarbage(t *testing.T) {
	_, err := events.DecodeExclusionEntry([]byte("not json"))
	assert.Error(t, err)
}

// The exclusion bucket is registered in the default topology, so a service
// that only calls Ensure(Default()) can reach it.
func TestImportExclusionBucketIsInTheDefaultTopology(t *testing.T) {
	var found bool
	for _, b := range events.Default().Buckets {
		if b.Name == events.BucketImportExclusions {
			found = true
			assert.Zero(t, b.TTL, "an exclusion is a standing block, not ephemeral progress")
		}
	}
	assert.True(t, found, "BucketImportExclusions missing from events.Default()")
}
