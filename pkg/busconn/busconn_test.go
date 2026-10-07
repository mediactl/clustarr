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
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/events/contracttest"
)

func TestConnectRejectsAnEmptyURL(t *testing.T) {
	_, _, err := busconn.Connect("", "catalogarr")
	require.ErrorContains(t, err, "busconn: empty NATS url")
}

func TestEnsureTopologyRejectsANilBus(t *testing.T) {
	require.ErrorContains(t, busconn.EnsureTopology(t.Context(), nil, contracttest.Topology()), "busconn: nil bus")
}

func TestReadyCheckerFailsWithoutNATS(t *testing.T) {
	err := busconn.ReadyChecker(nil, nil)(httptest.NewRequest("GET", "/readyz", nil))
	require.ErrorContains(t, err, "NATS is not configured")
}
