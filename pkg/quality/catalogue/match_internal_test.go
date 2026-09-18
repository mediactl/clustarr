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

package catalogue

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestMatchFailureMessage: matchTRaSH logged every MatchString error as
// "regexp2 match timed out", which is a lie for any other failure. regexp2
// exports no timeout sentinel (the error is a plain fmt.Errorf from
// runner.go), so the message is chosen from the one text it does produce.
func TestMatchFailureMessage(t *testing.T) {
	timeout := fmt.Errorf("match timeout after %v on input `%v`", 50*time.Millisecond, "Some.Release.2024")
	assert.Equal(t, "regexp2 match timed out", matchFailureMessage(timeout))
	assert.Equal(t, "regexp2 match timed out", matchFailureMessage(fmt.Errorf("wrapped: %w", timeout)))

	assert.Equal(t, "regexp2 match failed", matchFailureMessage(errors.New("something else entirely")))
	assert.Equal(t, "regexp2 match failed", matchFailureMessage(nil))
}
