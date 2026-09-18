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

package transcode_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
)

func TestBitrateForChannelsMatchesTheNotesPerChannelCountPolicy(t *testing.T) {
	// docs/research/transcode.md §5.2 table: mono 96k, stereo 160k(min 128k),
	// 5.1 384k (64k/ch), 7.1 512k. perChannelKbps=64 is the CRD default
	// (AudioSpec.BitratePerChannelKbps).
	cases := []struct {
		name           string
		channels       int32
		perChannelKbps int32
		want           int32
	}{
		{"mono floor applies", 1, 64, 96},
		{"stereo exactly the floor, no override needed", 2, 64, 128},
		{"5point1", 6, 64, 384},
		{"7point1", 8, 64, 512},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, transcode.BitrateForChannels(tc.channels, tc.perChannelKbps))
		})
	}
}
