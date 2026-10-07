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

package k8s_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// pkg/k8s's bus names are wrappers over pkg/busconn until Wave 5 deletes their
// last callers (spec §4.3 step 1.3).
func TestTheBusWrappersAreBusconn(t *testing.T) {
	_, _, err := k8s.ConnectBus("", "catalogarr")
	require.ErrorContains(t, err, "busconn: empty NATS url")
	require.Equal(t, busconn.DefaultNATSURL, k8s.DefaultNATSURL)
}
