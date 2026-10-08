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

package par2child

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Moved from pkg/download/usenet's TestTailWriterBoundsASubprocessThatTalksTooMuch
// with the child's output buffer (W9.3).
func TestTailBufferBoundsAChildThatTalksTooMuch(t *testing.T) {
	// The bound must not fail the write: an io.Writer that refuses output
	// makes exec.Cmd.Wait return THAT error instead of the child's outcome.
	w := &tailBuffer{max: 16}
	n, err := w.Write([]byte(strings.Repeat("a", 1000)))
	require.NoError(t, err)
	require.Equal(t, 1000, n, "the writer must claim every byte it was given")
	require.Len(t, w.String(), 16)

	n, err = w.Write([]byte("bbbb"))
	require.NoError(t, err)
	require.Equal(t, 4, n)
	require.Len(t, w.String(), 16)
	require.True(t, strings.HasSuffix(w.String(), "bbbb"), "the tail is what matters, got %q", w.String())
}
