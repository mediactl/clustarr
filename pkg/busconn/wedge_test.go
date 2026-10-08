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

package busconn_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/events/membus"
)

type wedged struct{ err error }

func (w wedged) Wedged() error { return w.err }

// TestWedgeCheckerReportsTheBus: the bus liveness check is the bus's own
// wedge report and nothing else -- it passes while the bus reports no
// wedged subscription (a live in-memory bus with none), fails with the
// report's text when it does, and passes for a process with no bus.
func TestWedgeCheckerReportsTheBus(t *testing.T) {
	assert.NoError(t, busconn.WedgeChecker(wedged{})(nil))
	assert.EqualError(t, busconn.WedgeChecker(wedged{errors.New("x wedged")})(nil), "x wedged")
	assert.NoError(t, busconn.WedgeChecker(nil)(nil))
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	assert.NoError(t, busconn.WedgeChecker(bus)(nil), "a bus with no subscription is not wedged")
}
