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

package grafttask_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/transcode/grafttask"
)

// TestAResultFitsATerminationMessage: a pod's termination message is 4096
// bytes; a result past it is cut and reads as no result at all (final
// review). HTML characters must not escape into six bytes each.
func TestAResultFitsATerminationMessage(t *testing.T) {
	r := grafttask.Failed(grafttask.ReasonMuxFailed, "%s", strings.Repeat("<&>é", 2000))
	r.Segments = make([]grafttask.Segment, 16)
	b := r.Encode()
	require.LessOrEqual(t, len(b), grafttask.MaxEncoded)
	got, err := grafttask.Decode(b)
	require.NoError(t, err)
	require.Equal(t, grafttask.ReasonMuxFailed, got.Reason)
}
